package showcase

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

const mb = 1024 * 1024
const gb = 1024 * mb

// Provider supplies a realistic multi-project VPS topology and live 1 Hz workload
// telemetry for local visual showcase and UI development.
type Provider struct {
	bus  *bus.Bus
	ring *ring.Store

	// actionDelay is the simulated daemon latency for container actions.
	actionDelay time.Duration
	opts        Options
	// specs holds inspect-level details per container name.
	specs map[string]dockerSpec

	mu        sync.RWMutex
	projects  []*asterismv1.Project
	workloads map[string]*asterismv1.Workload
	ports     []*asterismv1.PortBinding
	ingress   []*asterismv1.IngressRoute
	edges     []*asterismv1.TopologyEdge
	events    []*asterismv1.Event
	logs      map[string][]*asterismv1.LogLine
	tick      int64

	containers map[string]*asterismv1.Container // by name
	images     []*asterismv1.DockerImage
	volumes    []*asterismv1.DockerVolume
	networks   []*asterismv1.DockerNetwork
	engine     *asterismv1.DockerEngine
}

// New returns a provider with the curated showcase data set.
func New(b *bus.Bus, r *ring.Store) *Provider {
	return NewWithOptions(b, r, Options{})
}

// NewWithOptions returns a provider whose data set is sized by opts.
func NewWithOptions(b *bus.Bus, r *ring.Store, opts Options) *Provider {
	p := &Provider{
		bus:         b,
		ring:        r,
		actionDelay: 350 * time.Millisecond,
		opts:        opts,
		specs:       make(map[string]dockerSpec, len(showcaseSpecs)),
		workloads:   make(map[string]*asterismv1.Workload),
		logs:        make(map[string][]*asterismv1.LogLine),
	}
	for name, spec := range showcaseSpecs {
		p.specs[name] = spec
	}
	p.seedTopology()
	p.seedDocker()
	p.seedHistory()
	return p
}

func (p *Provider) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			p.step(now)
		}
	}
}

// The getters below return caller-owned deep copies. step() and
// ContainerAction mutate workloads, projects, and containers in place under
// p.mu, so handing out the internal pointers would let API readers race with
// the simulation once the read lock is released.

func (p *Provider) Projects() []*asterismv1.Project {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return dockercli.CloneAll(p.projects)
}

func (p *Provider) Workloads() []*asterismv1.Workload {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*asterismv1.Workload, 0, len(p.workloads))
	for _, proj := range p.projects {
		for _, w := range proj.Workloads {
			out = append(out, proto.Clone(w).(*asterismv1.Workload))
		}
	}
	return out
}

func (p *Provider) Workload(id string) (*asterismv1.Workload, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	w, ok := p.workloads[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(w).(*asterismv1.Workload), true
}

func (p *Provider) Ports() []*asterismv1.PortBinding {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return dockercli.CloneAll(p.ports)
}

func (p *Provider) IngressRoutes() []*asterismv1.IngressRoute {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return dockercli.CloneAll(p.ingress)
}

func (p *Provider) Edges() []*asterismv1.TopologyEdge {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return dockercli.CloneAll(p.edges)
}

func (p *Provider) Events() []*asterismv1.Event {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return dockercli.CloneAll(p.events)
}

func (p *Provider) Logs(workloadID string) []*asterismv1.LogLine {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := dockercli.CloneAll(p.logs[workloadID])
	if out == nil {
		out = []*asterismv1.LogLine{}
	}
	return out
}

func (p *Provider) seedTopology() {
	nowMs := time.Now().UnixMilli()
	startedHoursAgo := func(h int64) int64 { return nowMs - h*3600*1000 }

	addPort := func(id, wlID, wlName, projID, ip string, hostPort, ctrPort uint32, proto string, exp asterismv1.PortExposure, proc string) *asterismv1.PortBinding {
		pb := &asterismv1.PortBinding{
			Id:            id,
			WorkloadId:    wlID,
			WorkloadName:  wlName,
			ProjectId:     projID,
			HostIp:        ip,
			HostPort:      hostPort,
			ContainerPort: ctrPort,
			Proto:         proto,
			Exposure:      exp,
			ProcessName:   proc,
		}
		p.ports = append(p.ports, pb)
		return pb
	}

	// Project 1: Edge Gateway & Ingress (infra)
	wlCloudflared := &asterismv1.Workload{
		Id: "ctr:cloudflared", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER,
		Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_PROXY, Name: "cloudflared",
		ProjectId: "proj:edge-gateway", ServiceName: "cloudflared",
		Image: "cloudflare/cloudflared:2026.9.0", Pid: 1410, State: "running",
		Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, StartedAtMs: startedHoursAgo(340),
		CpuUtil: 0.014, MemUsedBytes: 42 * mb, MemLimitBytes: 256 * mb,
		NetRxBps: 420_000, NetTxBps: 380_000, Replicas: 1,
		Networks: []string{"edge_net"},
		EnvKeys:  []string{"TUNNEL_METRICS", "TUNNEL_TRANSPORT_PROTOCOL"},
		Labels:   map[string]string{"asterism.category": "infra", "asterism.role": "proxy"},
	}
	wlNginx := &asterismv1.Workload{
		Id: "unit:nginx.service", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT,
		Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_PROXY, Name: "nginx",
		ProjectId: "proj:edge-gateway", ServiceName: "nginx",
		Image: "nginx 1.28.0 (host systemd)", Pid: 984, State: "active",
		Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, StartedAtMs: startedHoursAgo(612),
		CpuUtil: 0.028, MemUsedBytes: 78 * mb, MemLimitBytes: 512 * mb,
		NetRxBps: 1_240_000, NetTxBps: 2_890_000, Replicas: 1,
		Ports: []*asterismv1.PortBinding{
			addPort("port:443", "unit:nginx.service", "nginx", "proj:edge-gateway", "0.0.0.0", 443, 443, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC, "nginx"),
			addPort("port:80", "unit:nginx.service", "nginx", "proj:edge-gateway", "0.0.0.0", 80, 80, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC, "nginx"),
		},
		Networks: []string{"host", "edge_net"},
		Labels:   map[string]string{"asterism.category": "infra", "asterism.role": "proxy"},
	}
	wlCaddy := &asterismv1.Workload{
		Id: "ctr:caddy-edge", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER,
		Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_PROXY, Name: "caddy-edge",
		ProjectId: "proj:edge-gateway", ServiceName: "caddy",
		Image: "caddy:2.10-alpine", Pid: 2048, State: "running",
		Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, StartedAtMs: startedHoursAgo(220),
		CpuUtil: 0.019, MemUsedBytes: 64 * mb, MemLimitBytes: 512 * mb,
		NetRxBps: 560_000, NetTxBps: 910_000, Replicas: 1,
		Ports: []*asterismv1.PortBinding{
			addPort("port:8443", "ctr:caddy-edge", "caddy-edge", "proj:edge-gateway", "0.0.0.0", 8443, 443, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC, "caddy"),
			addPort("port:2019", "ctr:caddy-edge", "caddy-edge", "proj:edge-gateway", "127.0.0.1", 2019, 2019, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK, "caddy"),
		},
		Networks: []string{"edge_net", "analytics_net", "staging_net"},
		EnvKeys:  []string{"CADDY_ADMIN"},
		Labels:   map[string]string{"asterism.category": "infra", "asterism.role": "proxy"},
	}

	// Project 2: Nova Core API (production)
	wlApiCore := &asterismv1.Workload{
		Id: "ctr:nova-api", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER,
		Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_APP, Name: "nova-api",
		ProjectId: "proj:nova-core", ServiceName: "api",
		Image: "ghcr.io/example/core-api:v2.4.1", Pid: 4120, State: "running",
		Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, StartedAtMs: startedHoursAgo(94),
		CpuUtil: 0.145, MemUsedBytes: 412 * mb, MemLimitBytes: 1 * gb,
		NetRxBps: 840_000, NetTxBps: 1_920_000, Replicas: 2,
		Ports: []*asterismv1.PortBinding{
			addPort("port:8080", "ctr:nova-api", "nova-api", "proj:nova-core", "127.0.0.1", 8080, 8080, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK, "core-api"),
		},
		Networks: []string{"edge_net", "core_net"},
		EnvKeys:  []string{"DATABASE_URL", "REDIS_URL", "PORT", "LOG_LEVEL"},
		Labels:   map[string]string{"asterism.category": "production"},
	}
	wlWorker := &asterismv1.Workload{
		Id: "ctr:nova-worker", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER,
		Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_WORKER, Name: "nova-worker",
		ProjectId: "proj:nova-core", ServiceName: "worker",
		Image: "ghcr.io/example/core-api:v2.4.1", Pid: 4312, State: "running",
		Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, StartedAtMs: startedHoursAgo(94),
		CpuUtil: 0.082, MemUsedBytes: 268 * mb, MemLimitBytes: 512 * mb,
		NetRxBps: 190_000, NetTxBps: 145_000, Replicas: 1,
		Networks: []string{"core_net"},
		EnvKeys:  []string{"DATABASE_URL", "REDIS_URL", "WORKER_CONCURRENCY"},
		Labels:   map[string]string{"asterism.category": "production"},
	}
	wlPostgres := &asterismv1.Workload{
		Id: "ctr:postgres-primary", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER,
		Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_DATABASE, Name: "postgres-primary",
		ProjectId: "proj:nova-core", ServiceName: "postgres",
		Image: "postgres:17.4-alpine", Pid: 3810, State: "running",
		Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, StartedAtMs: startedHoursAgo(612),
		CpuUtil: 0.064, MemUsedBytes: 896 * mb, MemLimitBytes: 2 * gb,
		NetRxBps: 520_000, NetTxBps: 780_000, Replicas: 1,
		Ports: []*asterismv1.PortBinding{
			addPort("port:5432", "ctr:postgres-primary", "postgres-primary", "proj:nova-core", "127.0.0.1", 5432, 5432, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK, "postgres"),
		},
		Networks: []string{"core_net"},
		EnvKeys:  []string{"POSTGRES_DB", "POSTGRES_USER", "PGDATA"},
		Labels:   map[string]string{"asterism.category": "production"},
	}
	wlRedis := &asterismv1.Workload{
		Id: "ctr:redis-cache", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER,
		Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_CACHE, Name: "redis-cache",
		ProjectId: "proj:nova-core", ServiceName: "redis",
		Image: "valkey/valkey:8.1-alpine", Pid: 3920, State: "running",
		Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, StartedAtMs: startedHoursAgo(612),
		CpuUtil: 0.021, MemUsedBytes: 184 * mb, MemLimitBytes: 512 * mb,
		NetRxBps: 290_000, NetTxBps: 310_000, Replicas: 1,
		Ports: []*asterismv1.PortBinding{
			addPort("port:6379", "ctr:redis-cache", "redis-cache", "proj:nova-core", "", 0, 6379, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL, "valkey-server"),
		},
		Networks: []string{"core_net"},
		Labels:   map[string]string{"asterism.category": "production"},
	}

	// Project 3: Analytics Hub (production)
	wlAnalyticsWeb := &asterismv1.Workload{
		Id: "ctr:analytics-web", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER,
		Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_APP, Name: "analytics-web",
		ProjectId: "proj:analytics-hub", ServiceName: "web",
		Image: "ghcr.io/example/analytics-web:v1.9.0", Pid: 5120, State: "running",
		Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, StartedAtMs: startedHoursAgo(48),
		CpuUtil: 0.044, MemUsedBytes: 196 * mb, MemLimitBytes: 512 * mb,
		NetRxBps: 310_000, NetTxBps: 640_000, Replicas: 1,
		Ports: []*asterismv1.PortBinding{
			addPort("port:3000", "ctr:analytics-web", "analytics-web", "proj:analytics-hub", "", 0, 3000, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL, "node"),
		},
		Networks: []string{"analytics_net"},
		EnvKeys:  []string{"INGEST_API_URL", "POSTGRES_URL"},
		Labels:   map[string]string{"asterism.category": "production"},
	}
	wlIngest := &asterismv1.Workload{
		Id: "ctr:ingest-collector", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER,
		Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_WORKER, Name: "ingest-collector",
		ProjectId: "proj:analytics-hub", ServiceName: "ingest",
		Image: "ghcr.io/example/ingest:v1.9.0", Pid: 5304, State: "running",
		Health: asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED, RestartCount: 2, StartedAtMs: startedHoursAgo(6),
		CpuUtil: 0.215, MemUsedBytes: 448 * mb, MemLimitBytes: 512 * mb,
		NetRxBps: 620_000, NetTxBps: 410_000, Replicas: 1,
		Ports: []*asterismv1.PortBinding{
			addPort("port:9000", "ctr:ingest-collector", "ingest-collector", "proj:analytics-hub", "", 0, 9000, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL, "ingest"),
		},
		Networks: []string{"analytics_net", "core_net"},
		EnvKeys:  []string{"NATS_URL", "TIMESCALE_URL", "BATCH_SIZE"},
		Labels:   map[string]string{"asterism.category": "production"},
	}
	wlTimescale := &asterismv1.Workload{
		Id: "ctr:timescale-db", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER,
		Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_DATABASE, Name: "timescale-db",
		ProjectId: "proj:analytics-hub", ServiceName: "timescale",
		Image: "timescale/timescaledb:2.18-pg17", Pid: 5410, State: "running",
		Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, StartedAtMs: startedHoursAgo(340),
		CpuUtil: 0.092, MemUsedBytes: 640 * mb, MemLimitBytes: 1536 * mb,
		NetRxBps: 410_000, NetTxBps: 280_000, Replicas: 1,
		Ports: []*asterismv1.PortBinding{
			addPort("port:5433", "ctr:timescale-db", "timescale-db", "proj:analytics-hub", "", 0, 5432, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL, "postgres"),
		},
		Networks: []string{"analytics_net"},
		EnvKeys:  []string{"POSTGRES_DB", "POSTGRES_USER"},
		Labels:   map[string]string{"asterism.category": "production"},
	}
	wlNats := &asterismv1.Workload{
		Id: "ctr:nats-broker", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER,
		Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_QUEUE, Name: "nats-broker",
		ProjectId: "proj:analytics-hub", ServiceName: "nats",
		Image: "nats:2.11-alpine", Pid: 5520, State: "running",
		Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, StartedAtMs: startedHoursAgo(340),
		CpuUtil: 0.015, MemUsedBytes: 58 * mb, MemLimitBytes: 256 * mb,
		NetRxBps: 380_000, NetTxBps: 390_000, Replicas: 1,
		Ports: []*asterismv1.PortBinding{
			addPort("port:4222", "ctr:nats-broker", "nats-broker", "proj:analytics-hub", "", 0, 4222, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL, "nats-server"),
		},
		Networks: []string{"analytics_net"},
		Labels:   map[string]string{"asterism.category": "production"},
	}

	// Project 4: Staging Sandbox (staging)
	wlStagingApp := &asterismv1.Workload{
		Id: "ctr:staging-preview", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER,
		Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_APP, Name: "staging-preview",
		ProjectId: "proj:staging-lab", ServiceName: "preview",
		Image: "ghcr.io/example/core-api:sha-9c4f21a", Pid: 6180, State: "running",
		Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, StartedAtMs: startedHoursAgo(14),
		CpuUtil: 0.018, MemUsedBytes: 148 * mb, MemLimitBytes: 512 * mb,
		NetRxBps: 45_000, NetTxBps: 72_000, Replicas: 1,
		Ports: []*asterismv1.PortBinding{
			addPort("port:4173", "ctr:staging-preview", "staging-preview", "proj:staging-lab", "", 0, 4173, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL, "preview"),
		},
		Networks: []string{"staging_net"},
		EnvKeys:  []string{"DATABASE_URL", "APP_ENV"},
		Labels:   map[string]string{"asterism.category": "staging"},
	}

	// Host Asterism loopback port + SSH port for complete open-port visibility
	addPort("port:22", "host", "sshd", "", "0.0.0.0", 22, 22, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC, "sshd")
	addPort("port:7700", "host", "asterism", "proj:edge-gateway", "127.0.0.1", 7700, 7700, "tcp", asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK, "asterism")

	allWorkloads := []*asterismv1.Workload{
		wlCloudflared, wlNginx, wlCaddy,
		wlApiCore, wlWorker, wlPostgres, wlRedis,
		wlAnalyticsWeb, wlIngest, wlTimescale, wlNats,
		wlStagingApp,
	}
	for _, w := range allWorkloads {
		p.workloads[w.Id] = w
	}

	p.projects = []*asterismv1.Project{
		{
			Id: "proj:edge-gateway", Name: "edge-gateway", Kind: "compose", Category: "infra",
			WorkingDir: "/srv/infra/edge-gateway", Status: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, HealthScore: 99,
			Workloads: []*asterismv1.Workload{wlCloudflared, wlNginx, wlCaddy},
		},
		{
			Id: "proj:nova-core", Name: "nova-core", Kind: "compose", Category: "production",
			WorkingDir: "/srv/apps/nova-core", Status: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, HealthScore: 96,
			Workloads: []*asterismv1.Workload{wlApiCore, wlWorker, wlPostgres, wlRedis},
		},
		{
			Id: "proj:analytics-hub", Name: "analytics-hub", Kind: "compose", Category: "production",
			WorkingDir: "/srv/apps/analytics-hub", Status: asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED, HealthScore: 78,
			Workloads: []*asterismv1.Workload{wlAnalyticsWeb, wlIngest, wlTimescale, wlNats},
		},
		{
			Id: "proj:staging-lab", Name: "staging-lab", Kind: "compose", Category: "staging",
			WorkingDir: "/srv/staging/lab", Status: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, HealthScore: 100,
			Workloads: []*asterismv1.Workload{wlStagingApp},
		},
	}

	p.ingress = []*asterismv1.IngressRoute{
		{
			Id: "ingress:nginx/api.example.com", ProxyKind: asterismv1.ProxyKind_PROXY_KIND_NGINX,
			ProxyWorkloadId: "unit:nginx.service", Hostnames: []string{"api.example.com"},
			ListenPort: 443, Tls: true, PathPrefix: "/v1", Strategy: "least_conn",
			Upstreams: []*asterismv1.UpstreamTarget{
				{WorkloadId: "ctr:nova-api", Address: "127.0.0.1:8080 (replica-1)", Weight: 3, Healthy: true},
				{WorkloadId: "ctr:nova-api", Address: "127.0.0.1:8082 (replica-2)", Weight: 2, Healthy: true},
			},
		},
		{
			Id: "ingress:nginx/vps.example.com", ProxyKind: asterismv1.ProxyKind_PROXY_KIND_NGINX,
			ProxyWorkloadId: "unit:nginx.service", Hostnames: []string{"vps.example.com"},
			ListenPort: 443, Tls: true, PathPrefix: "/", Strategy: "single",
			Upstreams: []*asterismv1.UpstreamTarget{
				{WorkloadId: "host", Address: "127.0.0.1:7700", Weight: 1, Healthy: true},
			},
		},
		{
			Id: "ingress:cloudflared/analytics.example.com", ProxyKind: asterismv1.ProxyKind_PROXY_KIND_CLOUDFLARED,
			ProxyWorkloadId: "ctr:cloudflared", Hostnames: []string{"analytics.example.com"},
			ListenPort: 443, Tls: true, PathPrefix: "/", Strategy: "tunnel",
			Upstreams: []*asterismv1.UpstreamTarget{
				{WorkloadId: "ctr:caddy-edge", Address: "caddy-edge:443", Weight: 1, Healthy: true},
			},
		},
		{
			Id: "ingress:caddy/analytics.example.com", ProxyKind: asterismv1.ProxyKind_PROXY_KIND_CADDY,
			ProxyWorkloadId: "ctr:caddy-edge", Hostnames: []string{"analytics.example.com"},
			ListenPort: 8443, Tls: true, PathPrefix: "/", Strategy: "round_robin",
			Upstreams: []*asterismv1.UpstreamTarget{
				{WorkloadId: "ctr:analytics-web", Address: "analytics-web:3000", Weight: 1, Healthy: true},
				{WorkloadId: "ctr:ingest-collector", Address: "ingest-collector:9000", Weight: 1, Healthy: true},
			},
		},
		{
			Id: "ingress:caddy/staging.example.com", ProxyKind: asterismv1.ProxyKind_PROXY_KIND_CADDY,
			ProxyWorkloadId: "ctr:caddy-edge", Hostnames: []string{"staging.example.com"},
			ListenPort: 8443, Tls: true, PathPrefix: "/", Strategy: "single",
			Upstreams: []*asterismv1.UpstreamTarget{
				{WorkloadId: "ctr:staging-preview", Address: "staging-preview:4173", Weight: 1, Healthy: true},
			},
		},
	}

	p.edges = []*asterismv1.TopologyEdge{
		{Id: "edge:cf-caddy", SourceId: "ctr:cloudflared", TargetId: "ctr:caddy-edge", Proto: "tcp", Port: 443, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_INGRESS, Source: asterismv1.EdgeSource_EDGE_SOURCE_PROXY, Label: "CF Tunnel → Caddy", ConnCount: 28, BytesPerSec: 410_000, Weight: 1},
		{Id: "edge:nginx-api", SourceId: "unit:nginx.service", TargetId: "ctr:nova-api", Proto: "tcp", Port: 8080, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_INGRESS, Source: asterismv1.EdgeSource_EDGE_SOURCE_PROXY, Label: "least_conn (2×)", ConnCount: 64, BytesPerSec: 1_840_000, Weight: 5},
		{Id: "edge:caddy-web", SourceId: "ctr:caddy-edge", TargetId: "ctr:analytics-web", Proto: "tcp", Port: 3000, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_INGRESS, Source: asterismv1.EdgeSource_EDGE_SOURCE_PROXY, Label: "HTTPS :3000", ConnCount: 18, BytesPerSec: 540_000, Weight: 1},
		{Id: "edge:caddy-staging", SourceId: "ctr:caddy-edge", TargetId: "ctr:staging-preview", Proto: "tcp", Port: 4173, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_INGRESS, Source: asterismv1.EdgeSource_EDGE_SOURCE_PROXY, Label: "staging TLS", ConnCount: 4, BytesPerSec: 52_000, Weight: 1},
		{Id: "edge:api-pg", SourceId: "ctr:nova-api", TargetId: "ctr:postgres-primary", Proto: "tcp", Port: 5432, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_DATABASE, Source: asterismv1.EdgeSource_EDGE_SOURCE_OBSERVED, Label: "pg :5432", ConnCount: 32, BytesPerSec: 690_000},
		{Id: "edge:api-redis", SourceId: "ctr:nova-api", TargetId: "ctr:redis-cache", Proto: "tcp", Port: 6379, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_CACHE, Source: asterismv1.EdgeSource_EDGE_SOURCE_OBSERVED, Label: "valkey :6379", ConnCount: 19, BytesPerSec: 310_000},
		{Id: "edge:worker-pg", SourceId: "ctr:nova-worker", TargetId: "ctr:postgres-primary", Proto: "tcp", Port: 5432, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_DATABASE, Source: asterismv1.EdgeSource_EDGE_SOURCE_OBSERVED, Label: "pg :5432", ConnCount: 8, BytesPerSec: 180_000},
		{Id: "edge:worker-redis", SourceId: "ctr:nova-worker", TargetId: "ctr:redis-cache", Proto: "tcp", Port: 6379, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_CACHE, Source: asterismv1.EdgeSource_EDGE_SOURCE_OBSERVED, Label: "queue :6379", ConnCount: 6, BytesPerSec: 120_000},
		{Id: "edge:web-ingest", SourceId: "ctr:analytics-web", TargetId: "ctr:ingest-collector", Proto: "tcp", Port: 9000, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_APP, Source: asterismv1.EdgeSource_EDGE_SOURCE_OBSERVED, Label: "RPC :9000", ConnCount: 12, BytesPerSec: 260_000},
		{Id: "edge:ingest-nats", SourceId: "ctr:ingest-collector", TargetId: "ctr:nats-broker", Proto: "tcp", Port: 4222, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_CACHE, Source: asterismv1.EdgeSource_EDGE_SOURCE_OBSERVED, Label: "NATS :4222", ConnCount: 14, BytesPerSec: 390_000},
		{Id: "edge:ingest-ts", SourceId: "ctr:ingest-collector", TargetId: "ctr:timescale-db", Proto: "tcp", Port: 5432, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_DATABASE, Source: asterismv1.EdgeSource_EDGE_SOURCE_OBSERVED, Label: "timescale :5432", ConnCount: 16, BytesPerSec: 440_000},
		{Id: "edge:web-ts", SourceId: "ctr:analytics-web", TargetId: "ctr:timescale-db", Proto: "tcp", Port: 5432, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_DATABASE, Source: asterismv1.EdgeSource_EDGE_SOURCE_STATIC, Label: "read :5432", ConnCount: 5, BytesPerSec: 110_000},
	}

	p.events = []*asterismv1.Event{
		{Id: "evt-1", TsMs: nowMs - 4*60*1000, EntityId: "ctr:ingest-collector", EntityName: "ingest-collector", Kind: "alert.memory", Severity: "warning", Message: "Memory reached 87.5% of 512 MiB cgroup limit"},
		{Id: "evt-2", TsMs: nowMs - 19*60*1000, EntityId: "unit:nginx.service", EntityName: "nginx", Kind: "ingress.reload", Severity: "info", Message: "Reloaded upstream pool api.example.com (2 healthy backends)"},
		{Id: "evt-3", TsMs: nowMs - 42*60*1000, EntityId: "ctr:nova-api", EntityName: "nova-api", Kind: "container.health", Severity: "info", Message: "Health check passed for replicas 1/2 and 2/2 (HTTP 200 in 4ms)"},
		{Id: "evt-4", TsMs: nowMs - 95*60*1000, EntityId: "ctr:ingest-collector", EntityName: "ingest-collector", Kind: "container.restart", Severity: "warning", Message: "Container restarted after OOM kill (exit code 137)"},
		{Id: "evt-5", TsMs: nowMs - 180*60*1000, EntityId: "ctr:caddy-edge", EntityName: "caddy-edge", Kind: "tls.renew", Severity: "info", Message: "ACME certificate verified for staging.example.com (84d remaining)"},
	}

	for id, w := range p.workloads {
		p.logs[id] = []*asterismv1.LogLine{
			{TsMs: nowMs - 32_000, WorkloadId: id, Stream: "stdout", Text: fmt.Sprintf("level=info service=%s msg=\"listening and accepting connections\"", w.Name)},
			{TsMs: nowMs - 18_000, WorkloadId: id, Stream: "stdout", Text: fmt.Sprintf("level=info service=%s latency_ms=3.4 status=200 path=/healthz", w.Name)},
			{TsMs: nowMs - 6_000, WorkloadId: id, Stream: "stdout", Text: fmt.Sprintf("level=info service=%s active_conns=14 mem_mb=%d", w.Name, w.MemUsedBytes/mb)},
		}
	}
	p.logs["ctr:ingest-collector"] = append(p.logs["ctr:ingest-collector"],
		&asterismv1.LogLine{TsMs: nowMs - 3_000, WorkloadId: "ctr:ingest-collector", Stream: "stderr", Text: "level=warn service=ingest-collector msg=\"batch buffer near watermark\" mem_pct=87.5 token=[REDACTED]"},
	)

	p.seedFleetLocked(p.opts.Fleet, nowMs)
	p.seedEventHistoryLocked(nowMs)
	p.recomputeProjectTotalsLocked(8 * gb)
}

func (p *Provider) seedHistory() {
	now := time.Now()
	// Pre-populate 90 seconds of smooth live history so charts render immediately.
	for i := 90; i >= 1; i-- {
		t := now.Add(-time.Duration(i) * time.Second)
		tsMs := t.UnixMilli()
		phase := float64(90-i) * 0.14

		p.mu.RLock()
		for idx, proj := range p.projects {
			wave := 1.0 + 0.08*math.Sin(phase+float64(idx)*0.9)
			p.ring.Append(proj.Id, "cpu.util", tsMs, clamp(proj.CpuUtil*wave, 0.005, 0.98))
			p.ring.Append(proj.Id, "mem.used_bytes", tsMs, float64(proj.MemUsedBytes)*(1.0+0.02*math.Cos(phase+float64(idx))))
			for j, w := range proj.Workloads {
				wwave := 1.0 + 0.12*math.Sin(phase+float64(idx+j)*0.7)
				p.ring.Append(w.Id, "cpu.util", tsMs, clamp(w.CpuUtil*wwave, 0.002, 0.98))
				p.ring.Append(w.Id, "mem.used_bytes", tsMs, float64(w.MemUsedBytes)*(1.0+0.02*math.Sin(phase*0.5+float64(j))))
			}
		}
		p.mu.RUnlock()
	}
}

func (p *Provider) step(now time.Time) {
	tsMs := now.UnixMilli()

	p.mu.Lock()
	p.tick++
	phase := float64(p.tick) * 0.18
	samples := make([]bus.MetricSample, 0, len(p.workloads)*2+len(p.projects)*2)

	for i, proj := range p.projects {
		for j, w := range proj.Workloads {
			if !p.containerRunningLocked(w.Name) {
				continue
			}
			deltaCPU := 0.008 * math.Sin(phase+float64(i*3+j)*0.85)
			w.CpuUtil = clamp(w.CpuUtil+deltaCPU*0.25, 0.004, 0.92)
			memJitter := int64(float64(mb) * 1.5 * math.Sin(phase*0.4+float64(j)))
			nextMem := int64(w.MemUsedBytes) + memJitter
			if nextMem < int64(24*mb) {
				nextMem = int64(24 * mb)
			}
			if w.MemLimitBytes > 0 && uint64(nextMem) > w.MemLimitBytes-8*mb {
				nextMem = int64(w.MemLimitBytes - 8*mb)
			}
			w.MemUsedBytes = uint64(nextMem)

			samples = append(samples,
				bus.MetricSample{EntityID: w.Id, Metric: "cpu.util", TSMs: tsMs, Value: w.CpuUtil},
				bus.MetricSample{EntityID: w.Id, Metric: "mem.used_bytes", TSMs: tsMs, Value: float64(w.MemUsedBytes)},
			)
		}
	}

	samples = append(samples, p.stepDockerLocked(now)...)

	p.recomputeProjectTotalsLocked(8 * gb)
	for _, proj := range p.projects {
		samples = append(samples,
			bus.MetricSample{EntityID: proj.Id, Metric: "cpu.util", TSMs: tsMs, Value: proj.CpuUtil},
			bus.MetricSample{EntityID: proj.Id, Metric: "mem.used_bytes", TSMs: tsMs, Value: float64(proj.MemUsedBytes)},
		)
	}
	p.mu.Unlock()

	p.bus.Publish(bus.Message{Samples: samples})
}

func (p *Provider) recomputeProjectTotalsLocked(hostMemTotal uint64) {
	if hostMemTotal == 0 {
		hostMemTotal = 8 * gb
	}
	for _, proj := range p.projects {
		var cpuSum float64
		var memUsed, memLimit uint64
		for _, w := range proj.Workloads {
			cpuSum += w.CpuUtil
			memUsed += w.MemUsedBytes
			memLimit += w.MemLimitBytes
		}
		proj.CpuUtil = cpuSum
		proj.MemUsedBytes = memUsed
		proj.MemLimitBytes = memLimit
		proj.MemSharePct = (float64(memUsed) / float64(hostMemTotal)) * 100.0
		proj.WorkloadCount = uint32(len(proj.Workloads))
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
