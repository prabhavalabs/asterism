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
| `asterism agent token create --host <name>` | Enrols an `asterism-agent` host: prints its ingest token once and the `[[agents]]` entry |
| `asterism-agent run` *(separate binary)* | Collects on a VPS and pushes outbound to a hub (D22–D25) |

Layering rule: **collectors → bus → store → services → (api | mcp | cli)**. The API, MCP, and
CLI are thin adapters over a shared `service` layer. No business logic goes in handlers.

**Collector modes** (`[collect] mode` in `asterism.toml`; the UI reads it from
`Capabilities.source_mode`):

| Mode | Where data comes from | Use it when |
|---|---|---|
| `agent` (**recommended for new installs**) | One `asterism-agent` per VPS dials the hub and pushes facts and metrics; the hub assembles projects, roles and edges | Any real deployment, including a hub on the same VPS (the agent pushes to `http://127.0.0.1:7700`, h2c allowed on loopback), so there is one code path |
| `ssh` | The hub runs read-only commands over the system `ssh` client against one VPS | You cannot install anything on the VPS; kept and supported |
| `local` | Host metrics of the machine running the hub, no Docker inventory | Trying the UI on a laptop |
| `showcase` | Live host metrics plus a generated demo VPS (`showcase_fleet` scales it) | Demos, screenshots, UI and pagination tests |

**Hub and agent.** In agent mode the hub (`asterism serve`) treats agent data as "just another
collector": `internal/collect/agentingest` keeps one session per enrolled host, expands metric
rows onto the bus, and exposes a `HostView` that implements the same provider interfaces the
SSH bridge does, so the service, API, MCP, CLI and UI need no agent-specific read paths. The
agent never listens on a port; the hub never holds SSH keys or VPS credentials.

```text
 VPS                                          Hub (asterism serve, collect.mode = "agent")
 ┌───────────────────────────────┐  HTTPS/h2   ┌─────────────────────────────────────────┐
 │ asterism-agent (static, ~10MB)│ outbound    │ api/agent.go (ingest-token auth only)   │
 │  scheduler: host · cgroup ·   │ ──────────► │  └► agentingest.Registry (per host)     │
 │  docker · listeners · systemd │ AgentFrame  │      ├► bus.Publish(samples) → ring/live │
 │  · processes · pm2 · proxies  │ ◄────────── │      ├► assemble.Build(facts) → inventory│
 │  uplink: ring, backoff, acks  │ HubFrame    │      └► HostView → service providers    │
 │  actions: local allow-list    │             │ AgentAdminService.ListAgents (Settings) │
 └───────────────────────────────┘             └─────────────────────────────────────────┘
```

The protocol is host-aware (every hub structure is keyed by host id) while the UI stays
single-host: the primary host (`[collect.agent] primary_host`, default the only or first
enrolled agent) keeps unqualified entity ids; other hosts are ingested under
`@<host>/<entity>` until a host switcher lands.

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
| Icons | Hugeicons (`@hugeicons/react` + `@hugeicons/core-free-icons`) | Matches the shadcn lyra preset; per-icon ESM imports tree-shake |
| Technology glyphs | `simple-icons` (CC0), named imports in one lazily loaded module | Monochrome brand marks for project and container icons, tinted with design tokens; kept out of the initial bundle |
| Toasts | `sonner`, restyled with tokens and lazy-mounted | Action results (container start/stop/restart) without a custom notification system |
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
                status (healthy|degraded|down), labels,
                icon (kind configured|label|detected|monogram, slug, color_token, monogram, label)
Service         id, project_id, name, role (proxy|app|worker|database|cache|queue|storage|unknown),
                replicas[], ports[], networks[], depends_on[]
Workload        id, kind (container|systemd_unit|process), role, name, service_id?, image?,
                pid, state, health, restart_count, started_at, cgroup_path, labels,
                tech (technology slug detected from the image, e.g. "postgres"; also on Container)
PortBinding     id, workload_id?, host_ip, host_port, container_port?, proto (tcp|udp),
                exposure (public|loopback|internal), process_name?
IngressRoute    id, proxy_kind (nginx|caddy|traefik|cloudflared|haproxy), proxy_workload_id?,
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
AgentStatus     host_id, name, state (connected|stale|offline|never), last_seen, connected_since,
                agent_version, transport (bidi|unary), remote_addr, lag_ms, clock_skew_ms,
                stats (rss, cpu, frames sent/dropped, buffer, reconnects, series, transport),
                capabilities[], config_hash, last_error, actions_enabled, allowed_actions[],
                inventory_version, frames_rejected, bytes_received
```

Agent wire messages (`proto/asterism/v1/agent.proto`) reuse the existing types: the agent sends
normalised `Container`, `Workload` (systemd units and plain or pm2 processes, never
containers), `PortBinding`, `IngressRoute`, the Docker resource messages, `HostInfo`,
`FilesystemUsage`, and `ProjectRule` (resolved member ids from the agent's `projects:` YAML).
Metrics travel as `AgentMetrics` rows against a per-connection series dictionary (§8.8).

**Entity IDs** must be stable and readable: `host`, `proj:shop`, `svc:shop/api`,
`ctr:shop-api-1`, `unit:nginx.service`, `proc:billing` (agent process and pm2 workloads),
`ingress:nginx/vps.example.com`. Non-primary agent hosts prefix ids with `@<host>/`; `@` is
reserved.

**Metric names** (dot-separated, unit in suffix where ambiguous):
`cpu.util` (0–1), `cpu.throttled_ratio`, `mem.used_bytes`, `mem.rss_bytes`,
`mem.limit_bytes`, `io.read_bps`, `io.write_bps`, `net.rx_bps`, `net.tx_bps`,
`disk.used_ratio{mount}`, `load.1`, `pids.count`, `oom.count`.

Per-container live series (entity `ctr:<name>`, emitted only while the container runs):
`cpu.util` (Docker CPU% / 100, so 1.0 = one full core and values can exceed 1),
`mem.used_bytes`, `net.rx_bps`, `net.tx_bps`, `blk.read_bps`, `blk.write_bps`, `pids.count`.
Container block I/O uses the `blk.*` prefix (matching Docker's "Block I/O") rather than the
host-level `io.*` names.

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
- **HAProxy** (`haproxy.cfg`, `PROXY_KIND_HAPROXY`, ids `ingress:haproxy/...`): `frontend`,
  `backend` and `listen` sections; hostnames from `hdr(host)`, `hdr_dom(host)`,
  `req.hdr(host)` and `ssl_fc_sni` ACLs used by `use_backend`, `default_backend`, `bind`
  ports, `server` targets and the `balance` strategy. The UI labels it "HAProxy" with the
  HAProxy glyph wherever proxy kinds are shown.
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
- **Active alert count** (`GetOverview.active_alert_count`, the sidebar Events badge): warning
  and critical events over the whole retained event log that fall in the `alerts` kind group,
  the same rule as `ListEvents(kind_group = "alerts")`. Audit records and container lifecycle
  events (start, die, restart, health, …) are not alerts, even at warning severity.

### 6.10 Agent collectors (`asterism-agent`)
The agent is a separate static binary (`cmd/asterism-agent`, D22) with its own collectors in
`internal/agent/collect/*`. It links neither gopsutil nor the Docker SDK: `/proc` and cgroup
files are parsed directly and the Docker Engine API is reached over its unix socket with a
compiled-in endpoint allow-list. One scheduler goroutine runs every collector from a timer
heap; the Docker events stream is the only other collector goroutine.

| Collector | Reads | Default interval | Sends |
|---|---|---|---|
| host | `/proc/{stat,meminfo,loadavg,uptime,net/dev,diskstats}`, statfs; static `os-release`, `cpuinfo`, `uname` | 2 s | `HostInfo`, `FilesystemUsage`, host series (`cpu.util`, `cpu.steal`, `mem.*`, `swap.*`, `load.*`, `net.*_bps`, `disk.*`) |
| cgroup | per container or unit: `cpu.stat`, `memory.current`, `memory.stat`, `io.stat`, `pids.current`, `memory.events`; `/proc/<pid>/net/dev` | 5 s | `ctr:*` / `unit:*` series (`cpu.util`, `mem.*`, `net.*_bps`, `blk.*_bps`, `pids.count`) |
| docker | `/_ping`, `/info`, `/events`, `/containers/json`, `/containers/{id}/json`, `/images/json`, `/volumes`, `/networks`, `/system/df` | events, plus 5 min list and 1 h `system df` | `Container`, `DockerEngine`, images, volumes, networks, disk usage (env reduced to names on the agent) |
| listeners | `<root>/proc/1/net/{tcp,tcp6,udp,udp6}`; inode owners via `/proc/*/fd` for new inodes only | 15 s | raw `PortBinding` with a workload hint |
| systemd | `system.slice` cgroup tree, optional D-Bus | 30 s | `Workload{kind: SYSTEMD_UNIT}`, `unit.failed` events |
| processes / pm2 | `/proc/*/{stat,cmdline,exe}` against match rules; pm2 `pids/*.pid` only | 30 s | `Workload{kind: PROCESS, id: proc:<name>}` with redacted cmdlines |
| proxies | nginx (with includes), Caddy admin API or Caddyfile, HAProxy, cloudflared `ingress:`, manual `routes:` | 30 s when mtime or hash changes | `IngressRoute` with upstream addresses; the hub resolves targets |
| self | `/proc/self/stat`, `runtime/metrics` | heartbeat (10 s) | `AgentStats` |

Rates are computed on the agent from counter deltas. A series not set during a successful tick
is retired; a failed tick keeps its series. Hub hints (`AgentHints`) shorten intervals while a
browser watches and stretch them by `idle_factor` otherwise, always clamped to the YAML
`[min, max]`. Role, category, icon, edge and upstream resolution stay on the hub in
`internal/collect/assemble`, shared with the SSH bridge, so classifier changes never need an
agent upgrade (D24).

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
service DockerService {                                             // read-only Docker inventory
  rpc ListContainers(ListContainersRequest) returns (ListContainersResponse); // running AND stopped
  rpc GetContainer(GetContainerRequest) returns (GetContainerResponse);
  rpc ListImages(ListImagesRequest) returns (ListImagesResponse);
  rpc ListVolumes(ListVolumesRequest) returns (ListVolumesResponse);
  rpc ListNetworks(ListNetworksRequest) returns (ListNetworksResponse);
  rpc GetDiskUsage(GetDiskUsageRequest) returns (GetDiskUsageResponse);   // docker system df
}
service ActionsService {                                            // mutating, opt-in, audited
  rpc StartContainer(StartContainerRequest) returns (ContainerActionResponse);
  rpc StopContainer(StopContainerRequest) returns (ContainerActionResponse);
  rpc RestartContainer(RestartContainerRequest) returns (ContainerActionResponse);
  rpc PauseContainer(PauseContainerRequest) returns (ContainerActionResponse);
  rpc UnpauseContainer(UnpauseContainerRequest) returns (ContainerActionResponse);
}
```

`DockerService` is separate from `InventoryService` because its unit is the Docker object
(container, image, volume, network) with full inspect detail, while `InventoryService` models
the classified topology (projects, workloads, ports, ingress). Both share entity IDs
(`ctr:<name>`), so the UI links a `Workload` to its `Container` directly.

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

### 8.6 Docker inventory and container actions
**Collection (SSH bridge).** Every 30 s inventory poll runs `docker ps -aq --no-trunc` followed
by **one** batched `docker inspect` over all ids. The inspect `--format` template selects only
the fields Asterism needs and reduces `Config.Env` to variable *names* on the Docker host, so
environment values never cross the wire. The 5 s metrics poll runs `docker stats --no-stream`
(CPU, memory, PIDs) and reads exact byte counters from cgroup v2 `io.stat` and
`/proc/<pid>/net/dev` (falling back to Docker's rounded totals) to derive per-second rates.
Engine info, images, volumes, and networks are polled every 60 s on a separate goroutine;
`docker system df` (summary plus verbose volume sizes) every ~5 minutes. Showcase mode serves
matching in-memory data, including exited, OOM-killed, unhealthy, and crash-looping
containers.

**Redaction.** Labels with secret-looking keys are dropped (`redacted_label_count` reports how
many), URL credentials in label values are masked, and healthcheck output is redacted and
truncated. Volume and network driver options are never fetched. Command, entrypoint and
healthcheck arguments go through one shared rule set (`internal/collect/dockercli`:
`RedactArgs`, `RedactShellArgs`, `RedactText`), used by the SSH bridge, by the agent before it
encodes a frame and again by the hub on everything an agent sends. Rules are idempotent and
golden-tested in exec form and as shell strings (`sh -c "..."`, `CMD-SHELL`), which are
tokenized with POSIX quoting, rewritten only where a word changes, and reset program context
at `;`, `&&`, `||` and `|`:

| Rule | Example → result |
|---|---|
| Program-aware short flags (active after the program name, also behind `exec`, `sudo`, `sshpass`) | `redis-cli -a pw` → `-a [REDACTED]`; `mysql -ppw` / `-p pw` → `-p[REDACTED]` / `-p [REDACTED]` (all mysql/mariadb tools); `mongosh -p pw`; `sshpass -p pw ssh -p 22` → only the first `-p`; `docker login -p pw` (only after `login`) |
| User:password values | `curl -u bob:pw`, `-ubob:pw`, `-su bob:pw`, `--user=bob:pw`, `-U`/`--proxy-user` → `bob:[REDACTED]`; `curl -b`/`--cookie` → `[REDACTED]` |
| Secret-looking long flags | `--password x`, `--pass=x`, `--db-pass x`, `-password x`, `wget --http-password x` |
| Secret-looking `KEY=VALUE`, also inside option values | `-e DB_PASSWORD=x`, `--env=API_TOKEN=x`, `-eSECRET_KEY_BASE=x`, `--build-arg NPM_TOKEN=x`, `-Ddb.password=x`, `PGPASSWORD=x psql` |
| URL userinfo anywhere | `postgres://app:pw@db` → `app:[REDACTED]@db`; `redis://:pw@cache`; token-like users (`https://ghp_…@github.com`, 20+ characters) |
| Free text | `Authorization: Basic …`, `Bearer …`, `Cookie:` / `Set-Cookie:` values, `password=…`, `"password": "…"`, `token: …` |

Values that are only a variable reference (`$REDIS_PASSWORD`, `${PW}`) are kept, since they
name the secret without revealing it. Deliberately untouched: `-p` for `docker run`, `ssh`,
`ps`, PostgreSQL tools and `wget` (ports, pids, page requisites), `tail -n`, and flags whose
value is not a secret (`--password-file`, `--password-stdin`, `--no-auth`, `--auth-source`,
`TOKEN_URL`, `*_FILE`). A secret flag followed by another option (`--auth -v`) does not consume
it. Trade-off: `mysql -p db` redacts `db`, because mysql's prompting `-p` cannot be told apart
from a spaced password.

**Derived insights** (service layer): `oom_killed`, `restart_loop`, `unhealthy`,
`nonzero_exit`, `privileged`, `docker_socket`, `host_network`, `public_datastore`,
`no_memory_limit`, `no_restart_policy`. A 5 s watcher diffs Docker snapshots and records
lifecycle events (`container.start|die|oom|restart|restarting|health|pause|unpause|create|destroy`).

**Actions.** `ActionsService` enforces, in order:
1. `[actions] allow_actions = true`, otherwise `FailedPrecondition` with how to enable it.
2. The caller holds the `actions` scope (`PermissionDenied` otherwise).
3. The target resolves from `ctr:<name>`, a name, or a hex id prefix against current inventory
   (strict regex; `InvalidArgument`/`NotFound`). Docker commands only ever receive the
   inventory's validated 12-hex id, single-quoted; user input is never interpolated.
4. The target is not protected: the Asterism container itself (`asterism.self=true` label or
   the `asterism` image), `asterism.protected=true`, or `[actions] protected_containers`
   (`PermissionDenied`; `allow_protected = true` overrides).
5. State preconditions (pause needs running, unpause needs paused) and timeout ≤ 300 s.
6. One action per container at a time (`Aborted` if busy). The command runs detached from the
   client connection with a hard deadline, then the container is re-inspected and returned.

Every attempt by an authenticated caller (success, failure, or refusal) is recorded as an
`Event` with kind `audit.container.<action>` and a structured `ActionAudit` (actor, actor kind,
action, target, container id, result, error, duration, state before/after, remote address),
pushed on the live stream, and logged via `slog`. Audit and lifecycle events are excluded
from the active alert count (§6.9). Until the M4 SQLite store lands, audit and lifecycle events live in a bounded
in-memory log (2 000 entries) and do not survive restarts.

**Project icons** (service layer, `internal/service/projecticon.go`). Every `Project` carries a
resolved `ProjectIcon`; clients only render it. Precedence:
1. `[projects."<name>"] icon = "<slug>"` (and optional `color = "<token>"`) in `asterism.toml`.
2. The compose label `asterism.icon` (optional `asterism.color`) on any of the project's
   containers, running or stopped; the first by container id wins.
3. The project's dominant technology, detected from images by a slug catalogue
   (`internal/service/tech.go`): a project named after a technology wins, otherwise the highest
   category priority (apps > monitoring > runtimes > databases > queues > storage > proxies >
   caches), then container count, then memory.
4. A one- or two-letter monogram on a palette colour chosen by an FNV hash of the name.

Colours are design-token names only (`chart-1`…`chart-5`, `role-proxy|app|db|cache|worker`,
`primary`); anything else is ignored, so configuration can never inject CSS. Unknown slugs fall
through to the next source. `Workload.tech` and `Container.tech` carry the same detection per
image for per-container glyphs. The web UI maps slugs to `simple-icons` paths in one lazily
loaded chunk (`web/src/components/common/techGlyphs.ts`) and falls back to a category glyph.
`GetOverviewResponse.container_counts` feeds the sidebar's running/total badge.

**Capabilities.** `GetOverviewResponse.capabilities`, `ListContainersResponse.capabilities`, and
`GetContainerResponse.capabilities` carry `actions_enabled`, `can_perform_actions` (enabled and
caller has the scope), `actions_disabled_reason`, `container_actions`, `scopes`,
`docker_available`, and `source_mode`. Clients render action buttons only when
`can_perform_actions` is true.

### 8.7 Pagination, search, ordering, and facets
Every list is paged, searched, filtered, and sorted **on the server**, so a page is always
correct for the whole data set and the browser never holds an unbounded list.

**Messages.** `PageRequest { page_size, page }` and `PageInfo { page, page_size, total_size,
total_pages }`. `page_size` defaults to 25 and is clamped to 10–200; `page` is 1-based (values
below 1 mean 1). A page past the end returns no items with a correct `PageInfo`;
`total_pages` is at least 1. A request that omits `page` gets the first page at the maximum
size (200), so non-table callers such as the topology map and project lookups stay bounded
without changes.

**Requests.** Each List RPC takes `page`, a `query` (case-insensitive substring over the
obvious fields; for containers and ports a number also matches a port exactly), an AIP-132
`order_by`, and its filters:

| RPC | Filters | `order_by` fields (default) | Facets in the response |
|---|---|---|---|
| `ListProjects` | `category` | `name`, `category`, `memory`, `cpu`, `workloads`, `health` (`memory desc`) | `category_counts` |
| `ListWorkloads` | `project_id`, `role` | `name`, `project`, `role`, `state`, `cpu`, `memory`, `restarts` (`project, name`) | `role_counts`, `project_counts` |
| `ListContainers` | `project_id`, `states`, `attention_only` | `name`, `project`, `state`, `image`, `cpu`, `memory`, `uptime`, `restarts`, `created` (`project, name`) | `counts` (`ContainerCounts`), `project_counts`, `mem_used_bytes` |
| `ListImages` | `dangling_only`, `in_use_only` | `size`, `created`, `repository`, `containers` (`size desc`) | `counts` (`ImageCounts`), `total_size_bytes` |
| `ListVolumes` | `unused_only`, `in_use_only` | `name`, `size`, `created`, `containers`, `project` (`name`) | `counts` (`VolumeCounts`), `total_size_bytes` |
| `ListNetworks` | `user_defined_only` (hides `bridge`, `host`, `none`) | `name`, `driver`, `containers`, `created` (`name`) | `counts` (`NetworkCounts`: `total`, `builtin`, `user_defined`; they ignore `user_defined_only`) |
| `ListPorts` | `exposure` | `exposure`, `port`, `workload`, `project`, `proto` (`exposure, port`) | `exposure_counts` |
| `ListIngress` | — | `hostname`, `port`, `proxy`, `upstreams` (`hostname`) | — |
| `ListEvents` | `entity_id`, `severities`, `kind_group` | `ts`, `severity`, `kind`, `entity` (`ts desc`) | `severity_counts`, `kind_counts` |

`order_by` is a comma-separated list of `<field> [asc|desc]`; field names are case-insensitive.
Unknown fields, unknown directions, empty terms, and repeated fields return `InvalidArgument`
naming the allowed fields. Ordering is stable: every sort ends with an ascending tie-break on
the entity id, so items never move between pages on refetch. `ListEvents.limit` remains as a
legacy page size used only when `page` is omitted.

**Facets.** A facet count is computed over the items that pass every filter and the query
*except its own filter*, and ignores the page, so filter chips show what selecting them would
return. Sums such as `total_size_bytes` and `mem_used_bytes` cover every matching item, not
just the page. `ContainerCounts.attention` counts unhealthy, restarting, dead, and OOM-exited
containers (the `attention_only` filter).

**Logs.** `TailLogs` pages backwards with a timestamp cursor instead of numbered pages:
`before_ts_ms` (exclusive; 0 = newest) and `limit` (default 200, maximum 500). Lines come back
oldest first with `has_more` and `next_before_ts_ms`. A page never splits lines that share a
millisecond, so the exclusive cursor cannot skip lines (a single millisecond with more lines
than `limit` is returned whole).

**Implementation.** The generic helpers live in `internal/service/paging.go`: `paginate[T]`,
a per-resource `sorter[T]` (allow-listed fields, AIP-132 parser, id tie-break), named
`filter[T]` predicates with `matching` (all filters except one, for facets), and `countBy`.
Handlers in `internal/api` only forward the request message. Showcase mode serves ~32
containers, ~65 images, ~35 volumes, 15 networks, 185 events, and ~360 log lines per container
by default; `[collect] showcase_fleet = N` (or `ASTERISM_SHOWCASE_FLEET`, max 40) adds N
generated compose projects with containers, ports, ingress routes, volumes, and networks.
The web Networks tab defaults to user-defined networks (`netFilter=all` shows Docker's built-in
ones) and labels its toggle with `NetworkCounts`. Settings → Agents pages `ListAgents`
client-side because that RPC returns every enrolled agent (configuration order, a handful per
hub); it gains `PageRequest` if hubs ever enrol hundreds.

### 8.8 Agent transport (`AgentService`, `AgentAdminService`)

```proto
service AgentService {                       // agent token (scope "ingest") only
  rpc Stream(stream AgentFrame) returns (stream HubFrame);   // HTTP/2 bidi, primary
  rpc Push(PushRequest) returns (PushResponse);              // HTTP/1.1 fallback: frames up, acks down
  rpc Commands(CommandsRequest) returns (stream HubFrame);   // HTTP/1.1 fallback: hub → agent
}
service AgentAdminService {                  // read scope (session or API token)
  rpc ListAgents(ListAgentsRequest) returns (ListAgentsResponse);  // agents + primary_host_id
}
```

- **Frames.** `AgentFrame` carries `AgentHello`, `AgentMetrics`, `InventorySnapshot`,
  `InventoryDelta`, `AgentEvents`, `LogChunk`, `CommandResult` or `AgentHeartbeat`;
  `HubFrame` carries `HelloAck`, `Ack`, `AgentHints`, `ResyncRequest`, log requests,
  `CommandRequest` and pings. Bidi and unary share the same messages and hub session code.
- **Hello.** The hub answers with the inventory version it already holds for that agent
  session, so an agent that survived a hub restart sends only a delta; otherwise it sends a
  snapshot. The hub measures clock offset at Hello and rewrites timestamps when the skew
  exceeds 2 s (shown as a warning in Settings → Agents).
- **Metrics.** `AgentMetrics{dict, dict_groups, rows, row_groups, keyframe_rows}`: a row is one
  tick of one source group against a series dictionary resent in full on every connection;
  an absent series carries forward, `NaN` retires it, and a keyframe row lists every live
  series of its group. Columnar protobuf plus gzip keeps idle traffic around 100 B/s. The agent
  compresses at `gzip.BestSpeed` rather than connect's default level: a pooled writer holds
  about 0.8 MB of encoder state instead of about 1.1 MB, and on 2–6 KB frames the ratio loss
  is about 10% of a stream that is already ~100 B/s.
- **Sequence and acks.** Only metrics and events frames carry `seq`, monotonic per agent
  process across reconnects. The hub acks `upto_seq` (every 5 s or 32 frames; in unary mode
  the `Push` response is the ack), drops already-seen rows, and the agent frees buffered
  frames only after an ack. Inventory is state, not a log: it is never queued and is resent
  as a snapshot or delta on reconnect. Commands are never replayed; if the stream drops
  mid-command the hub returns `Unavailable` ("result unknown; inventory will refresh").
- **Buffering.** Bounded in-memory rings on the agent (metrics by bytes, 1 MiB; events, 500);
  overflow drops the oldest and increments `frames_dropped` / `events_dropped` (golden rule
  5). Buffered metrics replay oldest first.
- **Transport selection.** `hub.transport: auto` tries bidi first and falls back to unary on
  `Unimplemented`, HTTP/1.1 or a 502/505. The fallback is locked in for the process lifetime
  only after a unary handshake succeeds. Failures that say nothing about HTTP/2 support never
  trigger it: network-level errors (connection refused, DNS failure, dial timeout: the hub is
  down or restarting), `Unauthenticated`, `PermissionDenied`, an SPKI pin mismatch, or
  shutdown. Without this rule, a hub that came back between the bidi and unary attempts would
  lock a healthy agent to unary. Reconnects use full-jitter exponential backoff (1 s → 60 s
  cap).
- **Liveness.** Heartbeat every 10 s; an agent is `connected` while its stream is up,
  `stale` once it drops (within `[collect.agent] stale_after`, default 30 s, range 5 s–10 min),
  then `offline`, which raises one `agent.offline` event (and `agent.online` on recovery). A
  newer connection for the same host replaces the old one.
- **Caps per agent.** Frames over 1 MiB are skipped and acked (an oversized snapshot closes the
  session), more than 50 frames/s (burst 500) returns `ResourceExhausted`, and a session may
  hold at most 5,000 series; refused frames count as `frames_rejected`.
- **Logs** are on demand: the hub asks for the last 200 lines over the session with a 5 s
  timeout; follow is not wired yet.
- **Actions** need both gates (§13). `Capabilities.container_actions` is the intersection of
  the hub's actions and the agent's advertised `allowed_actions`; when none can run,
  `actions_disabled_reason` says why (agent offline, per-agent gate off, empty allow-list),
  and the UI shows it in the disabled-action tooltips. Audit attrs add
  `executed_by = agent:<name>`, `agent_audit_id` and `agent_request_id`.

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
- Cap lists with `truncated: true` and a narrowing hint. List tools map their optional `page`,
  `page_size`, `query`, and `order_by` arguments onto the same service calls and `PageRequest`
  rules as the API (§8.7), and report `total_size` so agents can tell how much was left out.
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
/                         Dashboard (home): KPI tiles, host telemetry, 2.5D Isometric Architecture Map
                          (with 2D Constellation toggle), project tiles, memory allocation, recent events
/topology                 The same PortalCanvas full width and window height (`?view=constellation`,
                          `?project=<name>` focuses one project; back/forward step between levels)
/projects                 Categorized project grid with memory & CPU cost breakdown
/projects/$projectId      Project detail: isometric mini-map, services, DB links, live charts, events
/containers               Docker containers (state/project filters, search, sortable table grouped by
                          project, live CPU/memory sparklines, row actions) plus Images, Volumes,
                          Networks, and Disk usage tabs (`?tab=images|volumes|networks|disk`)
/containers/$containerId  Container detail (name or id): actions, live CPU/memory/network/block-I/O
                          charts, facts, ports, networks, mounts, labels, command, env names,
                          healthcheck, insights, events, logs (`?tab=overview|config|events|logs`)
/workloads/$workloadId    Workload detail: resources, open ports, logs, inspect, neighbours (today the
                          workload inspector sheet; container workloads link to /containers/$id)
/ports                    Listening ports by exposure (default tab) and Ingress & domains
                          (`?tab=ingress`: hostname table + Nginx, Caddy, Traefik, Cloudflare routes)
/host                     Host deep-dive: CPU per core + steal, memory, disks, network, processes
/events                   Event timeline (`?kind=lifecycle|audit|alerts`, `?severity=critical|warning|info`);
                          `?kind=audit` is the audit log
/settings                 Appearance, session, collector, Agents, API & MCP (`?section=agents|mcp`
                          scrolls to and outlines that card), deployment; later auth tokens,
                          categories, alert rules
```
Tabs, filters and sections that the sidebar links to live in **URL search params**, validated
in `web/src/lib/routeSearch.ts` (outside the lazy page chunks) so deep links, back and forward
work; unknown values fall back to the default view and defaults are omitted from the URL.
Time range, view mode, category filter, and selections follow the same rule as they move into
the URL.

**Navigation (sidebar).** Five labelled groups separated by hairlines: **Overview** (Dashboard,
Topology map, Events with the active-alert count), **Workloads** (Projects with count,
Containers with running/total, Images, Volumes, Networks), **Network** (Ports with the public
port count, Ingress & domains with the route count), **Host** (Host vitals, Disk usage) and
**Administration** (Audit log, Agents in agent mode only with a connected/enrolled badge,
Settings, API & MCP). Items that share a path are told apart by
the search param they own (`isNavItemActive` in `web/src/components/layout/nav.ts`); nested
detail routes keep their parent item active. Counts come from `GetOverview` only. The footer
keeps the live host card and the user menu (theme, documentation link to the public
repository, sign-out). Collapsed, the sidebar is a 48px icon rail with a tooltip on every item
and the separators between groups; below `md` it is a sheet. Breadcrumbs add a trailing crumb
for a non-default tab or section (`breadcrumbSearch` route static data), for example
`Containers / Images` or `Events / Audit log`. The whole expanded sidebar fits a 900px-tall
window without scrolling; in agent mode the host card drops its cores/uptime row (memory on one
line) to make room for the Agents item.

**Agent mode surfaces** (`web/src/components/agents/`). One shared `ListAgents` query (10 s
refetch, low-rate data, so not routed through `LiveStore`) feeds: a "via agent `<name>` ·
`<lag>`" chip in the topbar and the host card status, amber while stale and red while offline
(the topbar "Live" pill turns neutral "No data" then); an Overview banner ("Agent `<name>`
offline since …; data is stale"); and Settings → Agents, a paged table (status with last seen,
version and transport, lag with a clock-skew warning above 2 s, inventory version,
dropped/rejected frames, agent RSS and CPU, allowed actions, capability chips) with an "Add
agent" panel (token command, hub TOML, systemd install script or compose snippet, `validate`).
Outside agent mode Settings shows a short "switch to agent mode" explainer. The agent client
(`lib/agentRpc.ts`) is loaded with a dynamic import so its descriptors stay out of the initial
bundle.

Container actions (`web/src/components/containers/useContainerActions.tsx`) are not optimistic:
Start/Pause/Unpause run at once, Stop/Restart open a confirmation (AlertDialog) naming the
container with a graceful-timeout choice; the row shows a spinner while the RPC runs, the cache
is updated from the returned container and then invalidated, and the result (or the mapped
Connect error) is shown as a toast. Disabled buttons stay visible with a tooltip carrying
`actions_disabled_reason` or the container's `protected_reason`. Table sparklines and live
numbers read the LiveStore imperatively (no per-second React state).

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
- **Colour & Tokens:** shadcn "lyra" style on the "stone" neutral base: warm near-black
  canvas (`oklch(0.147 0.004 49.25)`) and stone-900 surfaces in dark, white cards on a warm
  off-white page in light, slightly rounded corners, 1px rings (`foreground / 10%`) instead of
  shadows, and a subtle menu accent. The accent is Asterism blue (`oklch(0.53 0.2 260)` light,
  `oklch(0.56 0.2 260)` dark); the data palette runs blue → sky → teal → mint → green;
  dedicated status tokens (healthy = green, warning = amber, critical = red, info = blue) are
  always paired with icons/labels, and role accents stay distinct (proxy blue, app teal,
  worker pink, database violet, cache amber). All colours live in `styles/tokens.css` CSS
  variables (no hard-coded hex in components).
- **Corners:** one `--radius` knob (8px) drives the Tailwind scale (`xs` 2, `sm` 4, `md` 6,
  `lg` 8, `xl` 10px) and semantic radii used by components: `rounded-card` 8px (cards, KPI
  tiles, table frames, sheets on their inner edge, dialogs, menus, toasts, the topology canvas
  and nodes, the sidebar host card), `rounded-tile` 6px (inner tiles, code and log blocks,
  tooltips), `rounded-control` 8px with `rounded-control-item` 6px (inputs, search, segmented
  controls and tab lists, and their items and sliding indicators), `rounded-button` 10px and
  `rounded-button-sm` 9px (every button, icon buttons and the sidebar trigger included),
  `rounded-chip` 6px (badges, status pills, port/domain chips, count chips), `rounded-nav` 8px
  (sidebar items and their active tint), `rounded-menu-item` 6px (menu items inset 4px in the
  menu), `rounded-bar` 4px (meters and exposure bars; stacked segments are clipped by the track
  so only the outer ends round) and `rounded-avatar` 8px (avatars, logo tile, 32px+ project
  icons; smaller icon tiles use 2–4px). Status dots stay round. Focus rings are box shadows,
  so they follow each radius.
- **Typography:** Outfit for UI, Oxanium for headings (page, card and sheet titles) and KPI
  values (its digits are fixed-width), Geist Mono for metrics, ports, IDs, and logs, with
  `font-variant-numeric: tabular-nums` everywhere values update. No visible text below 12px.
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
  (`asterism token generate --name <n> --scope read|actions`), displayed once; only the
  SHA-256 digest is stored, in `[[auth.tokens]]` until the M4 SQLite store exists.
- **Scopes:** `read` (every read RPC) and `actions` (mutating RPCs). The admin session holds
  `read`, plus `actions` only when `allow_actions = true`; tokens hold exactly the scopes
  configured for them, and `actions` still requires `allow_actions = true`.
- **`docker.sock`:** mounting `:ro` does **not** make the Docker API read-only. Document both
  direct socket access and running behind a read-only `docker-socket-proxy` (GET + events/logs
  only).
- **Proxy config parsing:** read-only access to Nginx/Caddy routing configs only; explicitly
  ignore TLS private key files and auth secret files.
- **Secrets:** never return container environment values, secret-looking labels, or mount
  contents. Redact bearer tokens, API keys, URL
  credentials and `password=` patterns in log output, and credentials in command arguments
  (shared rule set, §8.6).
- **Mutating actions:** disabled by default; separately opt-in for web API and MCP; every
  action requires an `actions`-scoped caller, refuses protected containers (Asterism itself),
  and is recorded as an audit event (§8.6).
- **CSRF:** ConnectRPC POST requests + `SameSite=Strict` cookies + the
  `Connect-Protocol-Version` header, which `ActionsService` requires
  (`connect.WithRequireConnectProtocolHeader`).
- **Agents (push, outbound only).** The agent dials the hub and never listens. TLS is required
  except to loopback (or with `hub.tls.insecure_dev`, which logs a loud warning); optional
  SPKI pins, a private CA and mTLS cover hubs on a private network.
- **Agent tokens.** `asterism agent token create --host <name> [--allow-actions]` prints an
  `asta_` token once; only its SHA-256 digest is stored in `[[agents]]`. Agent tokens carry the
  `ingest` scope only: they are accepted solely by `AgentService`, get 401 on every read API,
  and are bound to one host name (and optionally a machine-id hash; a mismatch is
  `PermissionDenied`). Session and API tokens get 403 on `AgentService`. M4 moves agents into
  SQLite with trust-on-first-use machine binding and revocation.
- **Two-gate actions.** A container action through an agent needs the hub gates (`[actions]
  allow_actions`, the per-agent `allow_actions`, the caller's `actions` scope, the protected
  checks) **and** the agent gates (YAML `actions.enabled`, the action in `allow`, the name
  matching `containers` globs and not `protect`, and the target not being the agent itself).
  Containers outside the agent's allow-list arrive `protected` with the reason, so a
  compromised hub can do no more than each agent's local YAML allows. Environment overrides
  can only turn agent actions off.
- **Redaction on the agent.** Docker `Config.Env` is reduced to names inside the converter;
  label filtering, argument and log-text redaction run on the agent (YAML can add secret
  patterns, never remove the built-ins); the agent never opens TLS keys, cloudflared
  credentials or pm2 dumps. The hub re-applies redaction and refuses agent-sent `audit.*`
  events, so an agent cannot forge audit records.
- **Least privilege.** systemd: a dedicated `asterism-agent` user with the hardened unit in
  `deploy/agent/` (`ProtectSystem=strict`, `NoNewPrivileges`, `MemoryMax`, `CPUQuota`, system
  call filter), adding `CAP_SYS_PTRACE` and `CAP_DAC_READ_SEARCH` only for port owners and
  root-only proxy configs. Container: user 65532, `cap_drop: ALL`, `no-new-privileges`,
  read-only root and read-only bind mounts.
- **Docker socket proxy.** Recommended in front of the agent (for example a GET-only
  socket proxy allowing `/containers`, `/events`, `/images`, `/volumes`, `/networks`, `/info`,
  `/system/df` and `/_ping`; the five container `POST` actions only when actions are on). The
  agent's own Docker client compiles in only those endpoints, so even with a raw socket it
  cannot call `exec` or `create`.
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
  3. **Agent mode (recommended):** the hub runs anywhere (same VPS, a home server, a laptop on
     Tailscale) with `[collect] mode = "agent"`, and each VPS runs `asterism-agent`.
- **Agent packaging** (`deploy/agent/`):
  - Binary: `CGO_ENABLED=0 go build -trimpath -tags netgo,osusergo -ldflags "-s -w"
    ./cmd/asterism-agent`, `linux/amd64` and `linux/arm64`. A dependency deny-list test keeps
    gopsutil, the Docker SDK, SQLite, cobra, `x/net` and the hub packages out of it.
  - Install script (`deploy/agent/install.sh`, published as the release asset `install.sh`):
    `sh install.sh --hub URL --token-file PATH [--host NAME] [--version vX.Y.Z]`, plus
    `--base-url`, `--binary`, `--unit`, `--docker-group` / `--no-docker-group`, `--no-start`,
    `--dry-run`, `--uninstall [--purge]`. It detects the architecture, downloads
    `asterism-agent-linux-<arch>.tar.gz`, `asterism-agent.service` and `checksums.txt` and
    verifies each SHA-256 (and the `cosign` signature when available), installs
    `/usr/local/bin/asterism-agent`, creates the `asterism-agent` user, writes
    `/etc/asterism/agent.yaml` (only if absent) and `/etc/asterism/agent.token`
    (`0640 root:asterism-agent`), installs the unit, runs `validate`, and enables it. The token is
    read from a file or the terminal, never from the command line (`--token` is refused).
  - systemd unit (`deploy/agent/asterism-agent.service`, installed as
    `/etc/systemd/system/asterism-agent.service`): `Restart=always`, `GOMAXPROCS=1
    GOMEMLIMIT=12MiB GOGC=50`, `MemoryHigh=48M`, `MemoryMax=64M`, `CPUQuota=10%`, `Nice=10`,
    idle I/O class, and the sandboxing in §13.
  - Container image (`deploy/agent/Dockerfile` → `ghcr.io/prabhavalabs/asterism-agent:0.2`,
    `FROM scratch`, user 65532, `ASTERISM_AGENT_HOST_ROOT=/host`) and
    `deploy/agent/docker-compose.agent.yml`: the agent (read-only root filesystem, no
    capabilities, `mem_limit: 32m`, `cpus: 0.1`) behind a GET-only `wollomatic/socket-proxy`,
    `/proc`, `/sys/fs/cgroup`, `os-release` and `machine-id` mounted read-only under `/host`,
    `agent.yaml` from `deploy/agent/examples/agent.compose.yaml`, and the token as a compose
    secret (`./agent.token`). Examples for bare-metal and mixed hosts sit next to it.
  - Profiling: `net/http/pprof` is linked only into builds with `-tags pprof`
    (`cmd/asterism-agent/pprof.go`; image `EXTRA_TAGS=pprof`), served on the loopback address in
    `ASTERISM_AGENT_PPROF`. Release builds and images contain no pprof and never listen.
  - Commands: `run` (default), `validate` (schema, sockets and paths, with fixes such as "grant
    DAC_READ_SEARCH"), `print-config` (token redacted, plus the `config_hash` sent in Hello),
    `discover` (dry run that suggests `projects:` YAML), `version`.

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
| Agent binary (stripped, `linux/amd64` and `linux/arm64`) | ≤ 11 MiB, CI gate (`ASTERISM_AGENT_SIZE_CHECK=1`); measured 10.76 MiB amd64, 10.00 MiB arm64 |
| Agent container image | ≤ 12 MB, CI gate (`make agent-image-size`) |
| Agent RSS, steady, 50 containers, default intervals | ≤ 15 MB, *measured* (below) |
| Agent CPU, 50 containers | ≤ 0.5% of one core avg, *measured* |
| Agent → hub traffic, 50 containers | ≤ 2 KB/s, *measured* |

The binary budget is 11 MiB because connect, protobuf and the stdlib TLS stack alone come to
about 9.6 MB; RSS does not depend on binary size. The image is the binary plus a CA bundle on
`scratch`.

**Agent footprint, measured** with `scripts/agent-footprint.sh` (`make agent-footprint`): the
hub in agent mode, a GET-only socket proxy, N idle containers and the hardened agent
container; 45 s warm-up, then a 5-minute window. Docker Desktop, arm64:

| Run | CPU (one core) | RSS, steady (`docker stats`) | Anon (heap) | Agent → hub (wire) |
|---|---|---|---|---|
| 50 containers, default (adaptive intervals) | 0.090% | 13.02 MB | 7.77 MB | 153 B/s |
| 10 containers, default | 0.063% | 14.65 MB | 10.74 MB | 128 B/s |
| 50 containers, watched rate (`ASTERISM_FP_ADAPTIVE=false`) | 0.192% | 15.08 MB | 9.40 MB | 238 B/s |

Method: CPU is the delta of the agent cgroup's `cpu.stat` `usage_usec` over wall time. RSS,
steady is the median of `docker stats` MemUsage samples over the second half of the window;
`docker stats` reports cgroup memory, which includes kernel memory (procfs, socket and slab
pages: 1.4–2.8 MB here) and, when this cgroup first faulted it in, page cache of the agent
binary, so it varies by a few MB from run to run. Anon is the cgroup's `anon` at the end of the
run (the Go heap and stacks proper). Traffic is the agent container's `eth0` tx bytes, so it
includes TCP/IP and HTTP/2 framing. The RSS budget applies to steady RSS at the default
(adaptive) intervals; the watched-rate run, where a browser keeps intervals at their minimum,
sits at the budget (15.08 MB) with anon memory well under it.

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
| D12 | First-class Nginx, Caddy, Traefik, HAProxy (`PROXY_KIND_HAPROXY`) and Cloudflare Tunnel ingress + port-map collector | Makes external domains, load-balancer upstreams, and exposed host ports immediately visible | Additional proxies (Envoy) become priority |
| D13 | Deterministic service role classification (`proxy`, `app`, `worker`, `database`, `cache`, `queue`, `storage`) | Distinct visual blocks for databases, caches, and proxies without manual config | Heuristic false positives require richer rule overrides |
| D14 | Apache-2.0 open-source license | Permissive for individuals and companies with explicit patent grant and trademark clarity | — |
| D15 | shadcn lyra look on the stone base (square corners, superseded by D20; 1px rings, Outfit + Oxanium, Hugeicons) re-tuned to Asterism blue/green, kept on Radix primitives; sidebar collapses to an icon rail with tooltips | Owner's preferred preset; Radix keeps the existing component APIs and avoids a Base UI migration; the rail keeps navigation one click away on narrow desktops | Base UI becomes the shadcn default for every component we use, or the rail hides information users need |
| D16 | Separate `DockerService` (read-only) and `ActionsService` (mutating) next to `InventoryService`; SSH bridge uses one batched `docker inspect` per 30 s poll with an env-names-only template, cgroup v2/netns byte counters for rates, and a slower resources/`system df` loop | Keeps read and write surfaces separately mountable and auditable; one inspect call keeps SSH overhead flat; env values never leave the host; exact counters give usable bps | A local `docker.sock` collector replaces the CLI-over-SSH path, or Docker events streaming makes polling unnecessary |
| D17 | Scope model `read`/`actions`: admin session gets `actions` only when `allow_actions = true`; static SHA-256-hashed bearer tokens in config; every action attempt audited as an `Event` with `ActionAudit`, held in a bounded in-memory log until M4 | Minimal, testable authorization that matches §13 without waiting for the SQLite store; refusals are visible in the Events page | M4 lands persistent `audit_log` and token storage, or multi-user accounts are needed |
| D18 | Project icons resolved server-side (config → `asterism.icon` label → dominant image technology → monogram) and sent as `Project.icon` with token-only colours; per-image `tech` slugs on `Workload`/`Container`; brand glyphs from `simple-icons` in a lazy chunk | One detection table in Go keeps web, CLI, and MCP consistent; token-only colours keep configuration from injecting styles; lazy glyphs keep the initial bundle under budget | Users want uploaded custom images, or the catalogue grows large enough to need a data file |
| D19 | Container management UI as a `/containers` page with a `/containers/$id` detail route (not a sheet), reusing UPlotChart/LiveStore and the logs RPC | Detail needs six live charts plus config sections and must be linkable from projects, ports, events, and the topology inspector | The workload inspector and container detail converge into one surface |
| D20 | Slightly rounded corners from one `--radius` (8px) through semantic radius tokens (card 8, tile 6, control 8/6, button 10/9, chip 6, bar 4), replacing the square lyra corners; sidebar regrouped into Overview / Workloads / Network / Host / Administration with deep links to URL-driven tabs and sections and a new full-window `/topology` route | Owner wanted a softer finish with buttons a touch rounder than cards, and a fuller, clearly categorised sidebar whose entries all lead to working views; URL-driven tabs make those links, back/forward and breadcrumbs consistent without new pages or bundle cost | The sidebar outgrows a 900px window (move secondary entries into collapsible groups), or a design refresh changes the corner language |
| D21 | Server-side pagination for every list: `PageRequest`/`PageInfo` (default 25, clamp 10–200, 1-based, past-the-end empty), `query`, AIP-132 `order_by` with per-resource allow-lists and an id tie-break, facet counts that ignore the page and their own filter, and a `before_ts_ms` cursor for logs; an omitted `page` returns the first 200 items | Lists must stay correct and bounded as a VPS accumulates hundreds of images, volumes, and events; doing it in `internal/service` keeps web, CLI, and MCP consistent; numbered pages suit tables, while a timestamp cursor suits append-only logs; the 200-item fallback keeps existing non-table callers working | A list routinely exceeds a few thousand items (move to opaque page tokens over the M4 SQLite store), or clients need live insertion without page drift |
| D22 | The agent is a **separate binary** (`cmd/asterism-agent`), not an `asterism agent` subcommand | The hub links the SPA, MCP, SQLite and gopsutil (~30 MB); the agent needs none of them and must stay small and auditable on every VPS. A dependency deny-list test enforces the split | The two binaries need to share enough code that release coordination costs more than the size saving |
| D23 | **Push, outbound only**: the agent dials the hub with a host-bound, ingest-only token and never listens | Works behind NAT, CGNAT and default-deny firewalls; the hub holds no SSH keys or root-equivalent VPS credentials; a stolen agent token can spoof one host's data but read nothing | Operators need pull-only collection where no outbound connection is allowed |
| D24 | **The agent sends facts; the hub assembles them**: normalised `Container`, `Workload`, `PortBinding`, `IngressRoute`, Docker resources and resolved project rules go up, and role, category, icon, edge and upstream resolution stay in `internal/collect/assemble`, shared with the SSH bridge | Classifier and topology changes ship with the hub alone, never requiring agent upgrades; SSH and agent modes classify identically | Hub-side assembly becomes a CPU bottleneck at many hosts |
| D25 | **Transport**: connect-rpc bidi `Stream` over HTTP/2 as the primary path, with unary `Push` plus a server-streaming `Commands` fallback for HTTP/1.1-only proxies; same frames on both, gzip, per-connection series dictionary, seq + ack only for replayable frames | One session implementation; works through any reverse proxy that already passes `LiveService` streams; acks bound memory on both ends without a disk queue | A transport (QUIC, WebTransport) becomes clearly cheaper, or unary overhead dominates at short flush intervals |
| D26 | The active alert count covers the whole retained event log with the `alerts` kind group (warning and critical, excluding audit and container lifecycle events), the same rule as `ListEvents(kind_group = "alerts")` | The sidebar badge and the Events "Alerts" filter must agree; routine lifecycle warnings (a container stopping) were inflating the count | Alert rules gain firing/resolved state (M4), at which point the count becomes "firing alerts" |
| D27 | `ListNetworks.user_defined_only` with `NetworkCounts` (`total`, `builtin`, `user_defined`, ignoring that filter); the web Networks tab defaults to user-defined | Docker's `bridge`, `host` and `none` networks are noise on most hosts but must stay one click away; server-side counts keep the toggle correct across pages | Users routinely need the built-in networks first |
| D28 | Agent budgets: binary ≤ 11 MiB, image ≤ 12 MB, steady RSS ≤ 15 MB at default intervals, CPU ≤ 0.5% of a core, ≤ 2 KB/s, all measured (§16); gzip at `BestSpeed`; `net/http/pprof` only with `-tags pprof` | connect, protobuf and TLS alone are ~9.6 MB of binary; `BestSpeed` saves ~0.3 MB of encoder state per pooled writer for ~10% more bytes on a ~100 B/s stream; release binaries must never listen | Steady RSS at default intervals exceeds 15 MB on reference hardware, or a cheaper compressor appears in connect |
| D29 | One shared, program-aware redaction rule set for arguments and text (§8.6), applied on the agent and again on the hub, with shell strings tokenized rather than split on whitespace | Short flags (`redis-cli -a`, `mysql -p`, `curl -u`, `sshpass -p`) and env args (`-e KEY=VALUE`) leaked through name-only rules; program context keeps `-p` as a port for `docker run`, `ssh` and `psql` | A credential form escapes the golden tables, or false positives hide information operators need |
