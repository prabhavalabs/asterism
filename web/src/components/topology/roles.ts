import { EdgeKind, EdgeSource, WorkloadRole } from "@/gen/asterism/v1/asterism_pb";

/** Colour family per workload role. Values are CSS variables from tokens.css. */
export type RoleFamily = "proxy" | "app" | "worker" | "db" | "cache";

export function roleFamily(role: WorkloadRole): RoleFamily {
  switch (role) {
    case WorkloadRole.PROXY:
      return "proxy";
    case WorkloadRole.WORKER:
      return "worker";
    case WorkloadRole.DATABASE:
    case WorkloadRole.STORAGE:
      return "db";
    case WorkloadRole.CACHE:
    case WorkloadRole.QUEUE:
      return "cache";
    default:
      return "app";
  }
}

/** `var(--role-*)` for inline SVG strokes and fills. */
export function roleColorVar(role: WorkloadRole): string {
  return `var(--role-${roleFamily(role)})`;
}

/** MeterBar tone for a role. */
export function roleMeterTone(
  role: WorkloadRole,
): "role-proxy" | "role-app" | "role-db" | "role-cache" | "role-worker" {
  return `role-${roleFamily(role)}`;
}

/** Background dot class per role family (full class names so Tailwind finds them). */
export const ROLE_DOT_CLASSES: Record<RoleFamily, string> = {
  proxy: "bg-role-proxy",
  app: "bg-role-app",
  worker: "bg-role-worker",
  db: "bg-role-db",
  cache: "bg-role-cache",
};

/** True for roles that hold state (databases, caches, queues, storage). */
export function isDataRole(role: WorkloadRole): boolean {
  return (
    role === WorkloadRole.DATABASE ||
    role === WorkloadRole.CACHE ||
    role === WorkloadRole.QUEUE ||
    role === WorkloadRole.STORAGE
  );
}

/** Stroke colour of an edge: ingress blue, app teal, database violet, cache/queue amber. */
export function edgeColorVar(kind: EdgeKind): string {
  switch (kind) {
    case EdgeKind.INGRESS:
      return "var(--role-proxy)";
    case EdgeKind.DATABASE:
      return "var(--role-db)";
    case EdgeKind.CACHE:
      return "var(--role-cache)";
    case EdgeKind.NETWORK:
      return "var(--muted-foreground)";
    default:
      return "var(--role-app)";
  }
}

export function edgeKindLabel(kind: EdgeKind): string {
  switch (kind) {
    case EdgeKind.INGRESS:
      return "Ingress";
    case EdgeKind.DATABASE:
      return "Database";
    case EdgeKind.CACHE:
      return "Cache / queue";
    case EdgeKind.NETWORK:
      return "Network";
    default:
      return "Service call";
  }
}

export function edgeSourceLabel(source: EdgeSource): string {
  switch (source) {
    case EdgeSource.STATIC:
      return "Inferred from config";
    case EdgeSource.PROXY:
      return "Proxy route";
    case EdgeSource.OBSERVED:
      return "Observed traffic";
    default:
      return "Unknown";
  }
}
