# Asterism — Project Blueprint

> **Asterism**: a small, informal pattern of stars.
> A self-hosted observatory and visual infrastructure portal for a single VPS (later: a handful
> of them) that shows your projects as crisp, interactive isometric blocks and a living
> constellation — what runs where, which ports are open, how much memory each project costs,
> how reverse proxies balance traffic, which databases each service connects to, and how the
> machine is coping — and exposes all of it over a CLI and MCP server.

This document is the source of truth for architecture and technical decisions. When an
implementation decision changes, update this file in the same pull request and record a row in
the decision log (§19).

Research snapshot: November 2026. Exact dependency versions are pinned at M0 scaffold time and
recorded in §14.

---

## 0. Table of contents

1. Vision and non-goals
2. What makes Asterism different
3. Architecture at a glance
4. Tech stack (with reasons)
5. Data model
6. Collectors (how we get the data)
7. Storage
8. API and real-time transport
9. MCP server (agent interface)
10. CLI
11. Frontend architecture
12. Design system and visual language
13. Security model
14. Repository layout and versions
15. Build, packaging, deployment
16. Testing and performance budgets
17. Roadmap (milestones)
18. Open-source reference projects to study
19. Decision log

---

## 1. Vision and non-goals

### Vision
- One small static binary on the VPS (accessible behind your reverse proxy at a subdomain such
  as `vps.example.com` or `monitor.example.com`). Open a browser, and in under a second you see
  every project grouped by category, every open port, how much memory each project costs, how
  Nginx/Caddy/Cloudflare route and balance traffic, which databases each service talks to, and
  how the host is coping — rendered as crisp, interactive 2.5D isometric blocks with smooth
  live charts.
- The same live and historical data is available to terminal operators (`asterism status`,
  `top`, `logs`, `topo`) and to MCP clients. An operator or MCP client can ask "why is the API
  slow?" and inspect CPU, memory, logs, topology, open ports, and recent events in one place.
- It should look and feel like a carefully engineered product, not a cluttered admin panel.

### Primary user
A developer running 5–50 containers and host services across several Docker Compose projects,
reverse proxies (Nginx, Caddy, Traefik, Cloudflare Tunnel), and databases on one or a few
VPSes.

### Non-goals (v1)
- Kubernetes. (Design the data model so it could be added later.)
- Being a Prometheus/Grafana replacement for large fleets. No PromQL, no plugin marketplace.
- Long-term metric retention beyond ~30 days.
- APM / distributed tracing. Asterism sees connections, routes, and ports, not application
  spans.
- Full remote deployment orchestration (builds, `compose up/down`) in v1. Mutating workload
  actions are opt-in, scoped, and audited (see §9.4 and §13).

---

## 2. What makes Asterism different

Existing tools to learn from (see §18): Beszel (lightweight hub/agent monitoring with Docker
stats), dashdot (polished single-server dashboard), Netdata, Uptime Kuma, Dozzle (logs),
Coroot (eBPF service maps). Asterism stands out in five ways:

1. **Projects and categories first, not flat container lists.** Group containers by Compose
   project (`com.docker.compose.project`) and category (`production`, `staging`, `infra`,
   `personal`, or custom labels/config), with systemd services and host processes as
   first-class citizens and clear per-project memory/CPU cost.
2. **Crisp 2.5D isometric block portal + 2D constellation topology.** An interactive visual map
   built from sharp SVG/CSS isometric blocks (no heavy WebGL/Three.js runtime) that clearly
   differentiates reverse proxies/load balancers, apps, workers, databases, caches, and open
   ports — with a toggle to a 2D constellation graph.
3. **First-class ingress, load balancing, and port visibility.** Parses Nginx, Caddy, Traefik
   labels, and Cloudflare Tunnel (`cloudflared`) routing alongside live host/container open
   ports so external-to-database traffic paths are immediately legible.
4. **Agent-native via MCP.** An official Go MCP server is built on top of the exact same
   service layer as the UI from day one.
5. **Taste and speed.** Fast, quiet, legible. Motion conveys live traffic and health state,
   never decoration.

---

## 3. Architecture at a glance

```text
                ┌──────────────────────────────────────────────────────────┐
                │                    asterism (Go binary)                  │
                │                                                          │
  /proc, /sys ──┤  collectors/                       api/                  │
  cgroup v2  ───┤   host      ──┐                    ConnectRPC handlers ──┼──► Browser SPA
  docker.sock ──┤   docker    ──┤                    subscription hub    ──┼──► (embedded, same origin)
  nginx/caddy ──┤   ingress   ──┼──► bus (pub/sub) ──►                     │
  cloudflared ──┤   ports     ──┤        │                                 │
  journald   ───┤   netflow   ──┤        ▼                                 │
  systemd    ───┤   systemd   ──┘     store/                  mcp/         │
                │                      ring buffers (hot) ──► tools     ───┼──► MCP clients
                │                      SQLite (warm/cold)     resources    │   (stdio or Streamable HTTP)
                │                                                          │
                │  cli/  (same binary: `asterism status`, `asterism top` …)│
                └──────────────────────────────────────────────────────────┘
```

**One binary, several modes:**

| Command | What it does |
|---|---|
| `asterism serve` | Runs collectors, storage, API, embedded web UI, and the HTTP MCP endpoint |
| `asterism mcp` | MCP over **stdio**. Talks to the local `serve` daemon via its API (or runs collectors in-process if no daemon is running) |
| `asterism status / top / logs / topo / ports …` | CLI views that call the same API |
| `asterism agent` *(later, multi-host)* | Collector-only mode that streams to a remote hub |

Layering rule: **collectors → bus → store → services → (api | mcp | cli)**. The API, MCP, and
CLI are thin adapters over a shared `service` layer. No business logic goes in handlers.

---

## 4. Tech stack (with reasons)

### Backend — Go
| Concern | Choice | Why |
|---|---|---|
| Language | Go (latest stable) | Strong networking primitives, single static binary, readable, low memory |
| Host metrics | `github.com/shirou/gopsutil/v4` | Pure Go (no cgo); CPU, memory, disk, network, processes, conntrack on Linux |
| Docker | `github.com/moby/moby/client` + `github.com/moby/moby/api` | Starting with Docker v29 the old `github.com/docker/docker` module is deprecated. Use `client.New(client.FromEnv)`; API version negotiation is on by default |
| Container resource stats | Read cgroup v2 files directly (`/sys/fs/cgroup/...`) | Much cheaper than Docker's per-container `stats` stream; fall back to Docker stats API only when cgroup v2 is unavailable |
| RPC / API | ConnectRPC (`connectrpc.com/connect`, v1 stable) + Protobuf (Buf toolchain) | Schema-first; browser-native over HTTP/1.1 or HTTP/2; also speaks gRPC; server streaming works in browsers; generates TS clients |
| MCP | `github.com/modelcontextprotocol/go-sdk` (official, ≥ v1.7) | Official SDK; supports MCP spec **2026-07-28** (stateless Streamable HTTP) |
| Storage | `modernc.org/sqlite` (pure Go) + in-memory ring buffers | No cgo, keeping cross-compilation simple; plenty for one host and 30 days of downsampled data |
| Logging | `log/slog` | Standard library, structured |
| Config | TOML file + env overrides (`ASTERISM_*`) + flags | Simple, human-editable |
| CLI | `github.com/spf13/cobra` + `charmbracelet/lipgloss` / `bubbletea` for TUI views | Crisp terminal output matching the visual identity |
| Live network flows (phase 2) | `/proc/<pid>/net/tcp{,6}` per container netns, then optionally `github.com/cilium/ebpf` | Starts with zero kernel requirements; adds eBPF byte counts later (§6.6) |

### Frontend — React SPA (no Next.js)
**Decision:** a Vite + React SPA, built to static files and **embedded in the Go binary** with
`//go:embed`. This is an authenticated dashboard behind one origin with no SEO or SSR need, so
avoiding a Node runtime on the VPS keeps deployment and memory overhead minimal.

| Concern | Choice | Why |
|---|---|---|
| Build | Vite + React 19 + TypeScript (strict) | Fast dev server, static output |
| Routing | TanStack Router (file-based, `@tanstack/router-plugin/vite`) | Type-safe routes and search params (time range, category, view mode, filters in the URL) |
| Server state | TanStack Query | Caching, refetching, and deduplication for request/response data |
| Live state | Small external store (ring buffers) + `useSyncExternalStore` | 1 Hz × dozens of series would cause re-render storms in Query/React state (§11.3) |
| RPC client | `@connectrpc/connect-web` + `@bufbuild/protobuf` (protobuf-es) | Generated from the same `.proto` files as the Go server |
| UI kit | shadcn/ui + Tailwind CSS v4 + Radix/Base UI primitives | Owned components themed via CSS variables |
| Dense live time-series | **uPlot** | Tiny (~45 KB), canvas-based, built for time-series with streaming updates and synced cursors |
| Summary charts (bars, donuts, radial gauges, sparklines) | **shadcn charts (Recharts v3)** | Theme-aware for low-cardinality summaries; never used for dense 1 Hz series |
| Visual portal & topology canvas | **React Flow (`@xyflow/react` v12)** + crisp 2.5D isometric SVG/CSS block nodes & animated edges | Sharp vector geometry, crisp port badges and typography, pan/zoom/selection, no WebGL/Three.js overhead |
| Graph layout | `elkjs` (layered / isometric grid) + `d3-force` (constellation view) | Structured architectural tiers for isometric blocks; organic starfield for 2D constellation |
| Motion | `motion` (Framer Motion v12) | Layout transitions, number tickers |
| Icons | `lucide-react` | Matches shadcn |
| Tables | TanStack Table | Containers, open ports, processes, routes |
| Forms/validation | `zod` | URL search-param and form schema validation |

---

## 5. Data model

Define these in `proto/asterism/v1/*.proto`. These definitions are the single contract for web,
CLI, and MCP (MCP tools return JSON shaped from the same messages).

```text
Host            id, hostname, os, kernel, arch, cpu_model, cores, mem_total, uptime, boot_time
Project         id ("compose:<name>" | "systemd:<unit>" | "manual:<name>"), name, kind,
                category ("production" | "staging" | "infra" | "personal" | custom),
                working_dir, services[], mem_used_bytes, mem_limit_bytes, cpu_util,
                status (healthy|degraded|down), labels
Service         id, project_id, name, role (proxy|app|worker|database|cache|queue|storage|unknown),
                replicas[], ports[], networks[], depends_on[]
Workload        id, kind (container|systemd_unit|process), role, name, service_id?, image?,
                pid, state, health, restart_count, started_at, cgroup_path, labels
PortBinding     id, workload_id?, host_ip, host_port, container_port?, proto (tcp|udp),
                exposure (public|loopback|internal), process_name?
IngressRoute    id, proxy_kind (nginx|caddy|traefik|cloudflared), proxy_workload_id?,
                hostnames[], listen_port, tls, path_prefix, strategy (round_robin|least_conn|ip_hash|single),
                upstreams[] (target_workload_id?, address, weight, backup, healthy)
Edge            id, src (workload|ingress|external), dst (workload|external), proto, port,
                edge_kind (ingress|app|database|cache|network),
                first_seen, last_seen, conn_count, bytes_tx?, bytes_rx? (phase 2+),
                source: static|proxy|observed
MetricSeries    key = {entity_id, metric}  e.g. {host, cpu.util}, {ctr:abc, mem.rss_bytes}
Point           ts (unix ms), value (float64)
Event           ts, entity_id, kind (container.start|die|oom|restart|health|deploy|alert),
                message, attrs
LogLine         ts, workload_id, stream (stdout|stderr), text
Alert           id, rule, entity_id, severity, state (firing|resolved), since, summary
```

**Entity IDs** must be stable and readable: `host`, `proj:shop`, `svc:shop/api`,
`ctr:shop-api-1`, `unit:nginx.service`, `ingress:nginx/vps.example.com`.

**Metric names** (dot-separated, unit in suffix where ambiguous):
`cpu.util` (0–1), `cpu.throttled_ratio`, `mem.used_bytes`, `mem.rss_bytes`,
`mem.limit_bytes`, `io.read_bps`, `io.write_bps`, `net.rx_bps`, `net.tx_bps`,
`disk.used_ratio{mount}`, `load.1`, `pids.count`, `oom.count`.

---

## 6. Collectors

Every collector implements:

```go
type Collector interface {
    Name() string
    Interval() time.Duration         // 0 = event-driven
    Run(ctx context.Context, out chan<- bus.Message) error
}
```

Collectors publish to an in-process bus (typed topics, fan-out with bounded per-subscriber
buffers; **drop the oldest** for slow subscribers and never block collectors).

### 6.1 Host (1 s)
gopsutil v4: CPU (total + per core + steal%), memory/swap, load average, disk usage per mount,
disk I/O counters, network counters per interface, temperatures (gracefully optional on VPSes).
Compute rates (bps) from counter deltas in the collector.

### 6.2 Docker inventory and classification (event-driven + 30 s resync)
- Initial `ContainerList(All: true)` + `ContainerInspect` for each container.
- Subscribe to the `Events` stream (`start`, `die`, `oom`, `health_status`, `restart`). Convert
  each into `Event`s and inventory updates.
- Group into Projects via `com.docker.compose.project`, `.service`, `.project.working_dir`,
  and `.depends_on` labels.
- Project **category** comes from `asterism.category` label, config overrides in
  `asterism.toml`, or automatic defaults (`infra` for reverse proxies/monitoring/tunnels).
- **Service role classification** (`proxy`, `app`, `worker`, `database`, `cache`, `queue`,
  `storage`) runs deterministically from:
  1. Explicit `asterism.role` label (highest precedence).
  2. Image repository matching (e.g. `postgres`, `mysql`, `mariadb`, `mongo`, `clickhouse` →
     `database`; `redis`, `valkey`, `memcached` → `cache`; `nginx`, `caddy`, `traefik`,
     `haproxy`, `cloudflared` → `proxy`; `rabbitmq`, `nats`, `kafka` → `queue`; `minio` →
     `storage`).
  3. Well-known listening ports (5432, 3306, 6379, 27017, 5672, etc.).
- Networks: `NetworkList` / `NetworkInspect` to build the container IP table needed for
  topology and upstream resolution.

### 6.3 Container resources (1 s, cgroup v2)
For each running container, read from its cgroup directory:
`cpu.stat` (`usage_usec`, `nr_throttled`, `throttled_usec`), `memory.current`, `memory.stat`,
`memory.max`, `io.stat`, `pids.current`. Network counters come from `/proc/<init_pid>/net/dev`
(the container's netns). Fall back to Docker's `stats` API only when cgroup v2 is unavailable.
Aggregate container memory and CPU into per-project cost totals in the service layer.

### 6.4 Ingress, reverse proxies, and load balancers (30 s + file/event watch)
First-class discovery of how external traffic enters the VPS and balances across backends:
- **Nginx** (host `/etc/nginx/nginx.conf` + `sites-enabled/*` / `conf.d/*`, or inside a
  detected Nginx container): parse `server` blocks (`listen`, `server_name`, `ssl_certificate`
  presence), `location` blocks (`proxy_pass`), and `upstream` blocks (target servers, `weight`,
  `backup`, `least_conn` / `ip_hash` strategy). Read-only config parsing — never read private
  key files.
- **Caddy** (local Admin API `http://127.0.0.1:2019/config/` when enabled, `Caddyfile`
  parsing, and `caddy-docker-proxy` container labels): extract hostnames, `reverse_proxy`
  upstreams, load-balancing policy (`lb_policy`), and TLS status.
- **Traefik** (Docker labels `traefik.http.routers.*` and `traefik.http.services.*`): extract
  `Host(...)` rules, entrypoints, and backend ports.
- **Cloudflare Tunnel (`cloudflared`)**: detect running `cloudflared` container or systemd
  unit and parse non-secret ingress rules from `config.yml` (`hostname` → `service` target)
  when readable, showing Cloudflare Edge → Tunnel → local service/proxy edges.
- Resolve each upstream `host:port` against Docker container IPs, service names, and host
  listening ports to emit `IngressRoute` records and `source: proxy` edges.

### 6.5 Open ports (5 s)
- Combine Docker published port bindings with host listening sockets from `/proc/net/tcp`,
  `/proc/net/tcp6`, `/proc/net/udp`, and `/proc/net/udp6` (state `0A` = LISTEN) matched to
  owning PIDs/systemd units/containers.
- Classify exposure clearly:
  - `public` (`0.0.0.0` / `::` / public interface)
  - `loopback` (`127.0.0.1` / `::1`)
  - `internal` (container-only bridge/overlay port not published on the host)

### 6.6 Topology ("who talks to whom") — tiered discovery
Build in tiers so Asterism delivers value immediately with minimal privileges:

**Tier A — static + proxy edges (v1, no privileges beyond `docker.sock` + readable proxy configs)**
- Reverse-proxy and Cloudflare Tunnel routes from §6.4 → `external → proxy → service` edges
  (tagged `source: proxy`, with hostname, TLS, and load-balancing metadata).
- Compose `depends_on` → declared edge.
- Env-var key/host heuristics: inspect *only* non-secret hostname/endpoint parts of `*_HOST`,
  `*_URL`, `DATABASE_URL`, `REDIS_URL`, etc. in memory to resolve target service names and
  infer `app → database` / `app → cache` edges (`source: static`). Never store or return raw
  env values or credentials.
- Shared user-defined Docker networks → subtle "shared network" grouping when needed.

**Tier B — observed connections (v1.x, needs root or `CAP_SYS_PTRACE` to read container netns)**
- Every 2–5 s, parse `/proc/<init_pid>/net/tcp` and `tcp6` (`01` = ESTABLISHED, `0A` = LISTEN).
- Map local and remote IP:port pairs to workloads via the IP table (§6.2) and conntrack NAT
  table (gopsutil `netfilter_conntrack` or `github.com/ti-mo/conntrack`).
- Aggregate into `Edge{conn_count, first_seen, last_seen}`. Unseen edges fade out after N
  minutes. Gives verified live connections and counts without byte totals.

**Tier C — eBPF flows (opt-in, kernel ≥ 5.8 with BTF)**
- `github.com/cilium/ebpf` CO-RE program on `sock:inet_sock_set_state`, `tcp_sendmsg`, and
  `tcp_cleanup_rbuf`, mapping socket → cgroup ID → container.
- Adds live bytes/sec per edge to drive edge thickness and pulse velocity.

### 6.7 Logs (on demand, not persisted by default)
Stream from Docker logs API (`follow`, `since`, `tail`) and journald (`journalctl -o json -f`
or `github.com/coreos/go-systemd/sdjournal`). Keep an optional in-memory error ring (last 500
error lines per workload) so `diagnose` and MCP can inspect recent errors without opening a
live stream.

### 6.8 systemd (30 s)
Over D-Bus (`github.com/coreos/go-systemd/v22/dbus`): list configured/active units, state,
main PID, and cgroup path so host services (Nginx, Caddy, PostgreSQL from apt, `cloudflared`)
appear as first-class workloads and projects alongside Docker stacks.

### 6.9 Derived signals (in `internal/service/`)
- Health score per project (0–100) from workload health, restarts, CPU throttling, memory
  pressure (`memory.events` high/oom), and error-tail rate.
- Per-project memory cost share (% of total VPS RAM and % of container limits).
- Built-in alert rules (TOML-configurable): disk > 85%, mem > 90% for 5 min, container restart
  loop, OOM kill, health check failing, CPU steal > 20% (noisy-neighbour indicator on VPSes).

---

## 7. Storage

| Tier | Where | Resolution | Retention |
|---|---|---|---|
| Hot | In-memory ring buffers per series | 1 s | 15 min |
| Warm | SQLite | 10 s (avg/min/max) | 48 h |
| Cold | SQLite | 1 min (avg/min/max) | 30 days |

- Write path: collector → ring buffer (immediate) → downsampler goroutine flushes 10 s rollups
  into SQLite in batched transactions every 10 s. A nightly compaction job rolls 10 s rows
  into 1 min rows and prunes expired rows.
- Schema: `series(id INTEGER PK, entity TEXT, metric TEXT, UNIQUE(entity, metric))`,
  `points_10s(series_id, ts, avg, min, max)` with `PRIMARY KEY(series_id, ts)` and
  `WITHOUT ROWID` (same shape for `points_1m`), plus tables for `events`, `edges`, `alerts`,
  `audit_log`, and `settings`.
- Pragmas: `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`.
- Query path: the service selects the appropriate tier from the requested range/step and
  stitches the hot ring buffer onto the recent edge.
- Budget: < 200 MB on disk for 50 containers × ~12 metrics × 30 days.

---

## 8. API and real-time transport

### 8.1 Decision
**ConnectRPC over HTTP/2 (`h2c` locally, TLS behind Nginx/Caddy), Protobuf binary encoding,
one schema for web, CLI, and internal clients.**

Server streaming covers every live subscription (server pushes coalesced frames; client
re-subscribes when view filters change). ConnectRPC handlers also speak gRPC and gRPC-Web for
future remote-agent compatibility.

### 8.2 Services (sketch)

```proto
service InventoryService {
  rpc GetOverview(GetOverviewRequest) returns (Overview);           // host + projects + categories + health
  rpc ListProjects(ListProjectsRequest) returns (ListProjectsResponse);
  rpc GetProject(GetProjectRequest) returns (Project);
  rpc ListWorkloads(ListWorkloadsRequest) returns (ListWorkloadsResponse);
  rpc GetWorkload(GetWorkloadRequest) returns (WorkloadDetail);
  rpc ListPorts(ListPortsRequest) returns (ListPortsResponse);      // host + container open port map
  rpc ListIngress(ListIngressRequest) returns (ListIngressResponse);// Nginx/Caddy/Traefik/Cloudflare routes
}
service MetricsService {
  rpc QueryRange(QueryRangeRequest) returns (QueryRangeResponse);
  rpc TopN(TopNRequest) returns (TopNResponse);
}
service TopologyService {
  rpc GetGraph(GetGraphRequest) returns (Graph);                    // projects, roles, ports, ingress, edges
}
service EventsService {
  rpc ListEvents(ListEventsRequest) returns (ListEventsResponse);
}
service LogsService {
  rpc Tail(TailRequest) returns (stream LogLine);
}
service LiveService {
  // One multiplexed stream per browser tab.
  rpc Subscribe(SubscribeRequest) returns (stream LiveFrame);
}
```

### 8.3 Live stream (`LiveService.Subscribe`)
- Topics: `metrics:host`, `metrics:proj:<name>`, `metrics:ctr:*`, `graph`, `ports`, `events`,
  `alerts`.
- `LiveFrame { oneof { SeriesDict, MetricBatch, GraphDelta, PortDelta, Event, Alert, Heartbeat } }`.
- `MetricBatch` is columnar (`ts`, `series_id[]`, `value[]`) with interned series IDs via
  `SeriesDict` so traffic stays `< 2 KB/s` per tab with 50 containers.
- Coalescing: 1 s when visible, 5 s when hidden (Page Visibility API).
- Heartbeat every 15 s; dead stream after 30 s triggers exponential backoff reconnect with
  jitter.

### 8.4 Reverse-proxy configuration note
Streaming responses must not be buffered:
- **Nginx:** `proxy_buffering off;`, `proxy_http_version 1.1;`, extended `proxy_read_timeout`,
  `proxy_set_header Connection "";`.
- **Caddy:** works out of the box with `reverse_proxy 127.0.0.1:7700`.

### 8.5 Auth
See §13. All API calls require a session cookie (web) or a bearer token (CLI/MCP).

---

## 9. MCP server (agent interface)

### 9.1 Goals
- Works with any MCP-compatible client over stdio or HTTP.
- Read-only by default, compact, and token-efficient.
- Mirrors the UI concepts (projects, categories, workloads, open ports, ingress routes,
  topology, metrics, events, logs).

### 9.2 Transports
Served from the same Go `internal/mcp/` package using the official `go-sdk`:
1. **stdio:** `asterism mcp` (local on VPS or over SSH: `ssh my-vps asterism mcp`).
2. **Streamable HTTP:** `POST/GET https://<host>/mcp`, bearer-token protected, configured in
   **stateless** mode (`StreamableHTTPOptions{Stateless: true}`) per MCP spec 2026-07-28.
   Legacy HTTP+SSE is deprecated and not implemented.

### 9.3 Tools (v1)
Small, orthogonal toolset (~14 tools) with JSON Schemas from Go struct tags, concise
descriptions, and MCP annotations (`readOnlyHint`, `destructiveHint`, `idempotentHint`):

| Tool | Purpose | Notes |
|---|---|---|
| `overview` | Host vitals, project categories, memory cost, health, top offenders | Default first call; `< 1.5k` tokens |
| `list_projects` | Projects with category, status, service count, CPU/mem totals | |
| `get_project` | Services, roles, workloads, ports, ingress routes, recent events | |
| `list_workloads` | Filter by project/role/state/kind; sort by CPU/mem/restarts | |
| `get_workload` | Inspect workload: image, role, env **keys only**, mounts, limits, health | Never return env values |
| `list_ports` | Open host/container ports with exposure (`public`/`loopback`/`internal`) | |
| `list_ingress` | Nginx, Caddy, Traefik, and Cloudflare Tunnel routes + upstreams | |
| `query_metrics` | Entity + metrics + range + step → summary stats (`raw: true` capped at 500) | |
| `top` | Top N workloads or projects by metric over a window | |
| `get_topology` | Nodes + edges (optional focus entity, depth 1–2) | Includes `edge_kind` and `source` |
| `list_events` | Lifecycle events, OOMs, restarts, alerts in a window | |
| `tail_logs` | Last N lines (≤ 500) with optional `grep`, `since`, `stream` | Redacts obvious secrets |
| `disk_usage` | Mount usage + Docker breakdown (images, volumes, build cache) | |
| `diagnose` | Rule-based correlation of metrics, events, logs, and neighbours | Deterministic, no LLM |

**Opt-in mutating tools** (disabled unless `mcp.allow_actions = true`, `destructiveHint: true`):
`restart_workload`, `stop_workload`, `start_workload`. Every invocation writes an entry to
`audit_log` and emits an event in the UI.

### 9.4 Resources and prompts
- Resources: `asterism://overview`, `asterism://project/{id}`, `asterism://topology`,
  `asterism://ports`.
- Prompts: `investigate_incident(entity, window)` and `capacity_review()`.

### 9.5 Output rules
- Return structured JSON plus a concise text summary.
- Include units in field names (`cpu_pct`, `mem_bytes`), ISO-8601 timestamps, and reusable IDs.
- Cap lists with `truncated: true` and a narrowing hint.
- Return actionable errors (e.g. `"unknown entity 'ctr:api'; did you mean 'ctr:shop-api-1'?"`).

---

## 10. CLI

Single binary calling the local API (with in-process collector fallback for one-shot commands
when the daemon is stopped):

```text
asterism serve [--config /etc/asterism/asterism.toml]
asterism mcp
asterism status                         # coloured host + categorized project summary
asterism top [--by cpu|mem]             # live bubbletea TUI (1 s refresh)
asterism ps [--project shop]
asterism ports [--exposure public]      # open ports and owning workloads
asterism ingress                        # Nginx/Caddy/Traefik/Cloudflare routing table
asterism logs <workload> [-f] [--grep ERR]
asterism topo [--around svc:shop/api]   # ASCII topology graph
asterism events [--since 1h]
asterism token create|list|revoke
asterism version
```

Human-readable output by default; `--json` on every command for scripting.

---

## 11. Frontend architecture

### 11.1 Routes (TanStack Router, file-based)
```text
/                         Portal (home): 2.5D Isometric Architecture Map (with 2D Constellation toggle) + host vitals rail
/projects                 Categorized project grid with memory & CPU cost breakdown
/projects/$projectId      Project detail: isometric mini-map, services, DB links, live charts, events
/workloads/$workloadId    Workload detail: resources, open ports, logs, inspect, neighbours
/ports                    Live Open Ports & Ingress/Load-Balancer routing table (Nginx, Caddy, Traefik, Cloudflare)
/host                     Host deep-dive: CPU per core + steal, memory, disks, network, processes
/events                   Event and audit timeline
/settings                 Auth tokens, project categories, alert rules, retention, MCP connection snippets
```
Time range, view mode (`?view=isometric|constellation`), category filter, and selections live
in **URL search params** validated with `zod`.

### 11.2 Data flow
```text
ConnectRPC unary  ──► TanStack Query (inventory, ports, ingress, history, graph snapshots)
ConnectRPC stream ──► LiveStore (ring buffers, plain TS, outside React)
                          ├─► uPlot instances  (imperative setData, rAF-batched)
                          ├─► useLiveValue()   (useSyncExternalStore, throttled to 4 Hz for KPIs/blocks)
                          └─► queryClient.setQueryData for low-rate entities (graph/port deltas, events)
```

### 11.3 Performance rules
- **Never** put 1 Hz metric points into React state or the TanStack Query cache.
- Each chart owns its `uPlot` instance; create once and call `u.setData()` from a
  `requestAnimationFrame`-batched subscriber.
- Live numbers on KPI tiles and isometric block memory bars update via `useLiveValue` at most
  4 Hz with `tabular-nums`.
- History + live tail: fetch `MetricsService.QueryRange`, then append live points into the
  same buffer, deduplicated by timestamp.
- Code-split by route automatically via TanStack Router. Initial JS budget: **< 250 KB gzip**.
- Topology rendering: memoize isometric block nodes and edge components; update edge traffic
  in place without recreating nodes/edges. Animate traffic pulses with CSS/SVG `animateMotion`
  driven by CSS variables; disable per-edge animation above ~150 nodes or when
  `prefers-reduced-motion` is enabled.

### 11.4 Directory shape
```text
web/src/
  routes/                 # file-based TanStack Router routes
  components/ui/          # shadcn/ui components
  components/charts/      # UPlotChart, Sparkline, Gauge, AreaSummary (Recharts)
  components/topology/    # PortalCanvas, isometric/ (2.5D SVG/CSS blocks), constellation/, edges/, layout/
  live/                   # LiveStore, ring buffer, subscription manager, reconnect
  gen/                    # buf-generated TS (do not edit)
  lib/                    # ConnectRPC transport, query client, formatters (bytes, %, durations, ports)
  styles/                 # tokens.css, isometric.css, chart theme
```

---

## 12. Design system and visual language

**Concept: "precision night-sky observatory with architectural isometric blocks."** Dark-first
(with a full light theme). Calm deep surfaces, razor-sharp 2.5D geometry, luminous live data.

- **Isometric 2.5D visual language (primary portal view):**
  - Each **Project** is rendered as a crisp isometric platform/tray grouped by category,
    displaying project name, category pill, health indicator, and live memory cost bar
    (`342 MB / 1.0 GB` · `4.2% VPS`).
  - Inside each platform, workloads appear as **distinct, sharp SVG/CSS 2.5D isometric
    blocks**:
    - **Reverse Proxy / Load Balancer** (Nginx, Caddy, Traefik, Cloudflare Tunnel): faceted
      gateway prism at the ingress edge showing domain pills, TLS badge, and upstream split.
    - **App / API Service**: crisp layered block with replica stack indicator, live CPU/RAM
      fill bar, and attached **port chips** (`:8080`, `:443`).
    - **Database** (Postgres, MySQL, Mongo, etc.): stacked isometric cylinder block so
      database dependencies stand out immediately across projects.
    - **Cache / Queue** (Redis, Valkey, RabbitMQ): compact diamond/faceted prism block.
    - **Open Port badges**: color-coded by exposure (`public` amber/ice highlight with lock/globe
      icon, `loopback` subtle slate, `internal` muted) — clickable to jump to the Port Map.
  - **Edges & Load Balancing:** crisp isometric/orthogonal connector paths with travelling
    pulses when traffic flows; load-balanced upstreams fan out visibly from the proxy block
    with weight/replica badges; inferred static edges are dashed and muted; database links use
    a distinct subtle accent hue.
  - Clicking any block opens a **slide-over inspector drawer** with live uPlot mini-charts,
    open ports, connected databases/upstreams, recent events, live logs, and (when enabled)
    audited actions.
  - One-click toggle switches between **Isometric Blocks** and **2D Constellation** (organic
    starfield graph).
- **Colour & Tokens:** deep blue-black canvas (`oklch(0.16 0.02 260)`), lifted surfaces,
  hairline borders (`oklch(1 0 0 / 0.06)`), perceptually even OKLCH data palette (ice blue,
  violet, teal, amber, rose), and dedicated status tokens (healthy = teal, warning = amber,
  critical = rose) always paired with icons/labels. All colours live in `styles/tokens.css`
  CSS variables (no hard-coded hex in components).
- **Typography:** Geist Sans / Inter for UI, Geist Mono / JetBrains Mono for metrics, ports,
  IDs, and logs, with `font-variant-numeric: tabular-nums` everywhere values update.
- **Charts:** 1.5 px lines, vertical gradient fills fading to 0 opacity, subtle dotted gridlines
  (6% opacity), synchronized crosshairs across charts on a page (`uPlot` `cursor.sync`).
- **Motion & Accessibility:** 150–250 ms ease-out transitions, full `prefers-reduced-motion`
  support, WCAG AA contrast in both dark and light themes, keyboard navigation across topology
  blocks, and an accessible table view for ports and charts.

---

## 13. Security model

A monitor with access to `docker.sock` is effectively root. Treat it accordingly:

- **Bind to `127.0.0.1:7700` by default.** Expose externally only behind Nginx/Caddy/Cloudflare
  Tunnel with TLS, or via SSH tunnel / Tailscale. Public binding requires `--unsafe-public`
  plus TLS.
- **Auth:** first-run setup creates an admin password (argon2id). Web uses `HttpOnly`,
  `SameSite=Strict`, `Secure` (over TLS) session cookies. CLI/MCP use scoped bearer tokens
  (`asterism token create --scope read|actions`), stored hashed and displayed once.
- **`docker.sock`:** mounting `:ro` does **not** make the Docker API read-only. Document both
  direct socket access and running behind a read-only `docker-socket-proxy` (GET + events/logs
  only).
- **Proxy config parsing:** read-only access to Nginx/Caddy routing configs only; explicitly
  ignore TLS private key files and auth secret files.
- **Secrets:** never return container environment values, secret-looking labels, or mount
  contents. Redact bearer tokens, API keys, and `password=` patterns in log output.
- **Mutating actions:** disabled by default; separately opt-in for web API and MCP; every
  action requires an `actions`-scoped token and is recorded in `audit_log`.
- **CSRF:** ConnectRPC POST requests + `SameSite=Strict` cookies + custom header check for
  state-changing RPCs.
- **Supply chain:** `govulncheck` and `pnpm audit` in CI, pinned GitHub Actions, signed
  releases (`cosign`), SBOM.

---

## 14. Repository layout and versions

```text
asterism/
├── LICENSE                     # Apache-2.0
├── README.md
├── CONTRIBUTING.md
├── docs/BLUEPRINT.md           # this file
├── proto/asterism/v1/*.proto
├── buf.yaml / buf.gen.yaml     # generates Go (internal/gen) + TS (web/src/gen)
├── Makefile
├── cmd/asterism/main.go        # cobra root; subcommands wire into internal/*
├── internal/
│   ├── config/
│   ├── bus/                    # typed pub/sub
│   ├── collect/{host,docker,cgroup,ingress,ports,netflow,systemd,logs}/
│   ├── store/{ring,sqlite}/
│   ├── service/                # domain logic: inventory, metrics, topology, diagnose, alerts
│   ├── api/                    # ConnectRPC handlers (thin)
│   ├── mcp/                    # MCP tools/resources/prompts (thin)
│   ├── cli/                    # CLI + TUI views
│   ├── auth/
│   ├── web/                    # go:embed of web/dist + SPA fallback handler
│   └── gen/                    # generated protobuf/connect code (do not edit)
├── web/                        # Vite + React 19 SPA
├── deploy/{systemd,docker,nginx,caddy}/
├── scripts/                    # dev helpers: seed compose stack, load generator
└── .github/workflows/
```

**Version pins (recorded at scaffold time):**
- Go: `1.27.x` (local dev toolchain verified at `1.27.3`; `go.mod` minimum set at M0)
- Node / pnpm: `Node 24.x` (verified `24.19.0`) / `pnpm`
- Go libraries: `connectrpc.com/connect` `__`, `modelcontextprotocol/go-sdk` `≥1.7.0`,
  `moby/moby/client` `__`, `gopsutil` `v4.__`, `modernc.org/sqlite` `__`
- Web libraries: `React 19.x`, `TanStack Router` `__`, `TanStack Query 5.x`,
  `@xyflow/react 12.x`, `uPlot 1.6.x`, `Recharts 3.x`, `Tailwind CSS 4.x`

---

## 15. Build, packaging, deployment

- `make dev`: runs `air` (Go hot reload) and `vite` together; Vite proxies `/asterism.*` RPC
  paths and `/mcp` to the Go server.
- `make build`: `buf generate` → `pnpm build` → `go build -trimpath -ldflags "-s -w"` with
  `web/dist` embedded. Target binary size `< 30 MB`.
- Release with GoReleaser: `linux/amd64` + `linux/arm64` tarballs, `.deb`/`.rpm`, multi-arch
  container image, checksums, and `cosign` signatures.
- **Install methods:**
  1. Single binary + systemd unit (`Restart=on-failure`, `MemoryMax=256M`, `Nice=10`,
     `ProtectSystem=strict`, `ReadWritePaths=/var/lib/asterism`), bound to `127.0.0.1:7700`
     behind Nginx or Caddy at e.g. `vps.example.com`.
  2. Docker: `--pid=host`, `-v /var/run/docker.sock:/var/run/docker.sock`,
     `-v /proc:/host/proc:ro`, `-v /sys/fs/cgroup:/host/sys/fs/cgroup:ro`, plus read-only
     mounts for `/etc/nginx:ro` or `/etc/caddy:ro` when reverse proxies run on the host.
     Tier B topology adds `--cap-add SYS_PTRACE`; Tier C adds `CAP_BPF` + `CAP_PERFMON`.

---

## 16. Testing and performance budgets

**Tests**
- Go: table-driven unit tests; collectors parse fixtures (`testdata/proc/`,
  `testdata/cgroup/`, `testdata/nginx/`, `testdata/caddy/`, `testdata/cloudflared/`) so CI
  runs without Docker or root. Integration tests run against `scripts/seed-stack` (Nginx +
  Caddy + API + worker + Postgres + Redis + load generator).
- MCP: drive the server in-process with the `go-sdk` client and golden-file tool outputs.
- Web: Vitest for `LiveStore`, formatters, and isometric layout helpers; Playwright for main
  flows and visual regression snapshots (dark and light themes, reduced motion).

**Budgets (CI-enforced where possible)**
| Metric | Budget |
|---|---|
| Daemon RSS (50 containers) | < 80 MB |
| Daemon CPU (1 s collection, 50 containers) | < 2% of one core avg |
| Live stream per tab | < 2 KB/s |
| First meaningful paint (local) | < 800 ms |
| Initial JS | < 250 KB gzip |
| Dashboard interaction | 60 fps while 20 live charts update |
| MCP `overview` response | < 1,500 tokens |

---

## 17. Roadmap

**M0 — Skeleton (week 1)**
Buf + Protobuf contracts, ConnectRPC hello-world, Vite + React 19 + TanStack Router +
shadcn/ui + Tailwind v4, `//go:embed` SPA, `asterism serve`, CI. One live host CPU chart
end-to-end over `LiveService.Subscribe`.

**M1 — Host, Docker inventory, categories, and open ports (weeks 2–3)**
Host collector, Docker inventory + events + service role classifier, cgroup v2 stats,
host/container open-port collector, in-memory ring buffers, categorized projects grid with
memory cost, workload table, host page with synced uPlot charts.

**M2 — Ingress/load-balancer discovery + 2.5D Isometric Portal (weeks 4–5)**
Nginx, Caddy, Traefik, and Cloudflare Tunnel ingress collector; static/proxy topology edges;
crisp 2.5D isometric block portal over `@xyflow/react` (distinct proxy/app/DB/cache blocks,
project platforms, open-port chips, animated load-balancer fan-out, detail drawer) + 2D
constellation layout toggle + `/ports` view.

**M3 — MCP v1 + CLI (week 6)**
stdio + stateless Streamable HTTP MCP server, token auth, v1 tools (`overview` through
`disk_usage`), and `asterism status / ps / ports / ingress / logs / topo`.

**M4 — Storage + history (week 7)**
SQLite warm/cold tiers, 10 s / 1 min downsampling, range queries, time-range picker, events
and audit timeline.

**M5 — Observed topology (Tier B) + alerts + diagnose (weeks 8–9)**
Observed TCP connections via `/proc/<pid>/net/tcp{,6}` + conntrack, edge fading, built-in
alert rules, `diagnose` tool, project health scores.

**M6 — Polish, VPS deployment templates, and open-source launch (week 10)**
Light theme pass, empty/error states, Nginx + Caddy + systemd deploy configs, install script,
GoReleaser, demo media, public release.

**Later:** eBPF Tier C (bytes/sec per edge), multi-host hub/agent, notification channels
(Telegram/Discord/webhook), Podman, read-only k3s.

---

## 18. Open-source projects to study

| Project | Study for |
|---|---|
| **Beszel** (henrygd/beszel) | Lightweight Go architecture, Docker stats, low memory footprint |
| **dashdot** (MauriceNino/dashdot) | Visual polish of a single-server dashboard |
| **Dozzle** | Real-time Docker log streaming UX |
| **Coroot** (+ node-agent) | Service map, conntrack NAT resolution, eBPF flows |
| **Netdata** | Breadth of host metrics, per-second UX |
| **Uptime Kuma** | Onboarding, status UX |
| **Homepage / Homarr** | Docker label auto-discovery and project categorization |
| **cAdvisor** | Accurate cgroup v2 stat parsing |
| **uPlot demos** (`/demos/stream-*`, `sync-cursor`) | Streaming updates and synced cursors |
| **React Flow examples** (custom nodes, animated edges, ELK layout) | Interactive topology rendering |
| **modelcontextprotocol/go-sdk examples** | Typed tools, stateless Streamable HTTP |

---

## 19. Decision log

| # | Decision | Reason | Revisit if |
|---|---|---|---|
| D1 | Go single binary, embedded SPA | Minimal footprint, single deployment artifact | SSR or public marketing pages are needed |
| D2 | Vite + React 19 SPA, no Next.js | No Node runtime on the VPS; authenticated dashboard needs no SSR | — |
| D3 | ConnectRPC + Protobuf with server streaming | One typed contract; browser-native; gRPC-compatible | Client→server browser streaming is required |
| D4 | One multiplexed live stream per tab | Fewer connections, simpler backpressure | — |
| D5 | uPlot for dense live series, Recharts (shadcn) for summaries | 60 fps where data is dense, fast styling for low-cardinality charts | uPlot styling limits |
| D6 | Direct cgroup v2 reads over Docker stats API | Order-of-magnitude lower CPU overhead | cgroup v1 hosts require parity |
| D7 | Pure-Go SQLite with 3 retention tiers | Zero ops, easy cross-compilation, sufficient for 30 days on a VPS | Multi-host scale requires external TSDB |
| D8 | Topology discovery tiers A → B → C | Immediate value without special kernel capabilities | — |
| D9 | MCP via official `go-sdk`, stdio + stateless Streamable HTTP | Matches MCP 2026-07-28; SSH stdio gives zero-config remote auth | — |
| D10 | Read-only by default; actions opt-in + audited | `docker.sock` is root-equivalent | — |
| D11 | Crisp 2.5D isometric SVG/CSS blocks over `@xyflow/react` (no Three.js) | Razor-sharp text/port badges, low bundle size (< 250 KB gzip), 60 fps without WebGL overhead | Users demand free-orbit 3D camera |
| D12 | First-class Nginx, Caddy, Traefik, and Cloudflare Tunnel ingress + port-map collector | Makes external domains, load-balancer upstreams, and exposed host ports immediately visible | Additional proxies (HAProxy, Envoy) become priority |
| D13 | Deterministic service role classification (`proxy`, `app`, `worker`, `database`, `cache`, `queue`, `storage`) | Distinct visual blocks for databases, caches, and proxies without manual config | Heuristic false positives require richer rule overrides |
| D14 | Apache-2.0 open-source license | Permissive for individuals and companies with explicit patent grant and trademark clarity | — |
