import {
  HealthStatus,
  PortExposure,
  ProxyKind,
  WorkloadRole,
} from "@/gen/asterism/v1/asterism_pb";

export function formatBytes(bytes: number | bigint): string {
  const n = typeof bytes === "bigint" ? Number(bytes) : bytes;
  if (!Number.isFinite(n) || n <= 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let idx = 0;
  let value = n;
  while (value >= 1024 && idx < units.length - 1) {
    value /= 1024;
    idx++;
  }
  const decimals = value >= 100 || idx === 0 ? 0 : value >= 10 ? 1 : 2;
  return `${value.toFixed(decimals)} ${units[idx]}`;
}

export function formatBps(bps: number): string {
  if (!Number.isFinite(bps) || bps <= 0) return "0 B/s";
  return `${formatBytes(bps)}/s`;
}

export function formatPct(ratio: number, decimals = 1): string {
  if (!Number.isFinite(ratio)) return "0.0%";
  return `${(ratio * 100).toFixed(decimals)}%`;
}

export function formatUptime(seconds: number | bigint): string {
  const total = typeof seconds === "bigint" ? Number(seconds) : Math.floor(seconds);
  if (!Number.isFinite(total) || total <= 0) return "0m";
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const mins = Math.floor((total % 3600) / 60);
  if (days > 0) return `${days}d ${hours}h`;
  if (hours > 0) return `${hours}h ${mins}m`;
  return `${mins}m`;
}

export function formatRelativeTime(tsMs: number | bigint): string {
  const ms = typeof tsMs === "bigint" ? Number(tsMs) : tsMs;
  const diffSec = Math.max(0, Math.floor((Date.now() - ms) / 1000));
  if (diffSec < 60) return `${diffSec}s ago`;
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m ago`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h ago`;
  return `${Math.floor(diffSec / 86400)}d ago`;
}

export function roleLabel(role: WorkloadRole): string {
  switch (role) {
    case WorkloadRole.PROXY:
      return "Ingress / LB";
    case WorkloadRole.APP:
      return "App / API";
    case WorkloadRole.WORKER:
      return "Worker";
    case WorkloadRole.DATABASE:
      return "Database";
    case WorkloadRole.CACHE:
      return "Cache";
    case WorkloadRole.QUEUE:
      return "Queue";
    case WorkloadRole.STORAGE:
      return "Storage";
    default:
      return "Service";
  }
}

/**
 * Compile-time exhaustiveness guard for switches over generated enums. A new
 * enum value fails `tsc` until it is handled; at runtime (an older UI against
 * a newer daemon) it returns the fallback.
 */
function unhandled<T>(value: never, fallback: T): T {
  void value;
  return fallback;
}

export function proxyKindLabel(kind: ProxyKind): string {
  switch (kind) {
    case ProxyKind.NGINX:
      return "Nginx";
    case ProxyKind.CADDY:
      return "Caddy";
    case ProxyKind.TRAEFIK:
      return "Traefik";
    case ProxyKind.CLOUDFLARED:
      return "Cloudflare Tunnel";
    case ProxyKind.HAPROXY:
      return "HAProxy";
    case ProxyKind.UNSPECIFIED:
      return "Proxy";
    default:
      return unhandled(kind, "Proxy");
  }
}

/** Tech slug (brand glyph in `TechIcon`) for a proxy kind; `undefined` when unknown. */
export function proxyKindTech(kind: ProxyKind): string | undefined {
  switch (kind) {
    case ProxyKind.NGINX:
      return "nginx";
    case ProxyKind.CADDY:
      return "caddy";
    case ProxyKind.TRAEFIK:
      return "traefik";
    case ProxyKind.CLOUDFLARED:
      return "cloudflared";
    case ProxyKind.HAPROXY:
      return "haproxy";
    case ProxyKind.UNSPECIFIED:
      return undefined;
    default:
      return unhandled(kind, undefined);
  }
}

export function exposureLabel(exposure: PortExposure): string {
  switch (exposure) {
    case PortExposure.PUBLIC:
      return "Public";
    case PortExposure.LOOPBACK:
      return "Loopback";
    case PortExposure.INTERNAL:
      return "Internal";
    default:
      return "Unknown";
  }
}

export function healthLabel(status: HealthStatus): string {
  switch (status) {
    case HealthStatus.HEALTHY:
      return "Healthy";
    case HealthStatus.DEGRADED:
      return "Degraded";
    case HealthStatus.DOWN:
      return "Down";
    default:
      return "Unknown";
  }
}

/** Action durations: "340 ms", "1.2 s", "1m 5s". */
export function formatDurationMs(ms: number | bigint): string {
  const n = typeof ms === "bigint" ? Number(ms) : ms;
  if (!Number.isFinite(n) || n < 0) return "—";
  if (n < 1000) return `${Math.round(n)} ms`;
  if (n < 60_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)} s`;
  const mins = Math.floor(n / 60_000);
  const secs = Math.round((n % 60_000) / 1000);
  return `${mins}m ${secs}s`;
}

/** Absolute local timestamp for tooltips and fact grids. */
export function formatDateTime(tsMs: number | bigint): string {
  const n = typeof tsMs === "bigint" ? Number(tsMs) : tsMs;
  if (!Number.isFinite(n) || n <= 0) return "—";
  return new Date(n).toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
