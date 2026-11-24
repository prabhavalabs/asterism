# Asterism

A self-hosted VPS observatory and visual infrastructure portal. Asterism runs as a single static Go binary with an embedded web interface that maps your projects, open ports, memory cost, reverse-proxy routing, databases, and live host metrics—and exposes the same data over a CLI and MCP server.

## Overview

Asterism treats a VPS as a living system organized by **projects**, not a flat table of containers:

- **Visual isometric architecture map** — crisp 2.5D isometric blocks (plus a 2D constellation topology view) showing projects, service roles (reverse proxies, apps, workers, databases, caches), open ports, memory usage, and live traffic links.
- **Ingress and load-balancing visibility** — discovers reverse-proxy routing and upstreams across Nginx, Caddy, Traefik labels, and Cloudflare Tunnels alongside host and container listening ports.
- **Low-overhead live telemetry** — reads host vitals and container cgroup v2 counters directly, streaming coalesced updates over ConnectRPC into canvas-backed charts (`uPlot`) with tiered SQLite retention.
- **Operator CLI and MCP server** — inspect status, logs, topology, and diagnostics from the terminal or via MCP (stdio and stateless Streamable HTTP).
- **Read-only by default** — binds to `127.0.0.1:7700`, never exposes container environment values or secrets, and gates any mutating workload actions behind explicit opt-in configuration, scoped tokens, and audit logging.

## Planned Repository Layout

```text
asterism/
├── docs/               # Architecture blueprint and operational guides
├── proto/asterism/v1/  # Protobuf contracts (source of truth for API and types)
├── cmd/asterism/       # CLI entrypoint (`serve`, `mcp`, `status`, `top`, `logs`, `topo`)
├── internal/
│   ├── config/         # TOML + env + flag configuration
│   ├── bus/            # Bounded in-process pub/sub
│   ├── collect/        # Host, Docker, cgroup v2, ingress/proxy, netflow, systemd, logs
│   ├── store/          # In-memory ring buffers + tiered SQLite storage
│   ├── service/        # Domain logic: inventory, metrics, topology, diagnose, alerts
│   ├── api/            # ConnectRPC handlers
│   ├── mcp/            # MCP tools, resources, and prompts
│   ├── cli/            # CLI and TUI views
│   ├── auth/           # Session cookies and scoped bearer tokens
│   ├── web/            # Embedded static SPA handler
│   └── gen/            # Generated Go protobuf/ConnectRPC code
├── web/                # Vite + React 19 + TypeScript SPA
├── deploy/             # systemd, Docker, Nginx, and Caddy deployment configs
└── scripts/            # Local development and test-stack helpers
```

## What Is Not Versioned

Runtime databases (`*.db`, `*.sqlite*`), local configuration overrides (`asterism.local.toml`, `.env*`), private keys/certificates, build outputs (`bin/`, `web/dist/`), and dependency directories (`web/node_modules/`) are excluded from Git.

## Status

Asterism is in initial architecture and scaffolding phase. See [docs/BLUEPRINT.md](docs/BLUEPRINT.md) (added via the architecture PR) for the full design, data model, performance budgets, and milestone roadmap.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the branching model, commit conventions, and pull-request standards.

## License

Licensed under the [Apache License 2.0](LICENSE).
