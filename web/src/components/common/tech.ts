import { WorkloadRole } from "@/gen/asterism/v1/asterism_pb";

/**
 * Technology slugs resolved by the daemon (`Workload.tech`, `Container.tech`,
 * `ProjectIcon.slug`). Detection lives in internal/service/tech.go; this table
 * only adds presentation: a label and a category for colour and fallback glyph.
 */
export type TechCategory =
  | "app"
  | "runtime"
  | "monitoring"
  | "database"
  | "queue"
  | "storage"
  | "cache"
  | "proxy"
  | "platform";

export interface TechMeta {
  label: string;
  category: TechCategory;
}

export const TECH: Readonly<Record<string, TechMeta>> = {
  wordpress: { label: "WordPress", category: "app" },
  ghost: { label: "Ghost", category: "app" },
  nextcloud: { label: "Nextcloud", category: "app" },
  gitea: { label: "Gitea", category: "app" },
  forgejo: { label: "Forgejo", category: "app" },
  gitlab: { label: "GitLab", category: "app" },
  n8n: { label: "n8n", category: "app" },
  nodered: { label: "Node-RED", category: "app" },
  umami: { label: "Umami", category: "app" },
  plausible: { label: "Plausible", category: "app" },
  vaultwarden: { label: "Vaultwarden", category: "app" },
  keycloak: { label: "Keycloak", category: "app" },
  mattermost: { label: "Mattermost", category: "app" },
  metabase: { label: "Metabase", category: "app" },
  directus: { label: "Directus", category: "app" },
  strapi: { label: "Strapi", category: "app" },
  supabase: { label: "Supabase", category: "app" },
  appwrite: { label: "Appwrite", category: "app" },
  pocketbase: { label: "PocketBase", category: "app" },
  hasura: { label: "Hasura", category: "app" },
  immich: { label: "Immich", category: "app" },
  jellyfin: { label: "Jellyfin", category: "app" },
  homeassistant: { label: "Home Assistant", category: "app" },
  ollama: { label: "Ollama", category: "app" },
  mastodon: { label: "Mastodon", category: "app" },
  discourse: { label: "Discourse", category: "app" },
  listmonk: { label: "listmonk", category: "app" },
  ntfy: { label: "ntfy", category: "app" },
  pihole: { label: "Pi-hole", category: "app" },
  adguard: { label: "AdGuard Home", category: "app" },
  syncthing: { label: "Syncthing", category: "app" },
  jenkins: { label: "Jenkins", category: "app" },
  drone: { label: "Drone", category: "app" },
  portainer: { label: "Portainer", category: "app" },
  grafana: { label: "Grafana", category: "monitoring" },
  prometheus: { label: "Prometheus", category: "monitoring" },
  victoriametrics: { label: "VictoriaMetrics", category: "monitoring" },
  uptimekuma: { label: "Uptime Kuma", category: "monitoring" },
  sentry: { label: "Sentry", category: "monitoring" },
  opentelemetry: { label: "OpenTelemetry", category: "monitoring" },
  timescale: { label: "TimescaleDB", category: "database" },
  postgres: { label: "PostgreSQL", category: "database" },
  mysql: { label: "MySQL", category: "database" },
  mariadb: { label: "MariaDB", category: "database" },
  mongodb: { label: "MongoDB", category: "database" },
  clickhouse: { label: "ClickHouse", category: "database" },
  influxdb: { label: "InfluxDB", category: "database" },
  elasticsearch: { label: "Elasticsearch", category: "database" },
  opensearch: { label: "OpenSearch", category: "database" },
  meilisearch: { label: "Meilisearch", category: "database" },
  neo4j: { label: "Neo4j", category: "database" },
  cassandra: { label: "Cassandra", category: "database" },
  etcd: { label: "etcd", category: "database" },
  rabbitmq: { label: "RabbitMQ", category: "queue" },
  nats: { label: "NATS", category: "queue" },
  kafka: { label: "Apache Kafka", category: "queue" },
  minio: { label: "MinIO", category: "storage" },
  garage: { label: "Garage", category: "storage" },
  restic: { label: "restic", category: "storage" },
  rclone: { label: "rclone", category: "storage" },
  valkey: { label: "Valkey", category: "cache" },
  redis: { label: "Redis", category: "cache" },
  memcached: { label: "Memcached", category: "cache" },
  traefik: { label: "Traefik", category: "proxy" },
  caddy: { label: "Caddy", category: "proxy" },
  cloudflared: { label: "Cloudflare Tunnel", category: "proxy" },
  haproxy: { label: "HAProxy", category: "proxy" },
  envoy: { label: "Envoy", category: "proxy" },
  nginx: { label: "Nginx", category: "proxy" },
  apache: { label: "Apache httpd", category: "proxy" },
  tailscale: { label: "Tailscale", category: "proxy" },
  wireguard: { label: "WireGuard", category: "proxy" },
  node: { label: "Node.js", category: "runtime" },
  bun: { label: "Bun", category: "runtime" },
  deno: { label: "Deno", category: "runtime" },
  python: { label: "Python", category: "runtime" },
  go: { label: "Go", category: "runtime" },
  php: { label: "PHP", category: "runtime" },
  ruby: { label: "Ruby", category: "runtime" },
  java: { label: "Java", category: "runtime" },
  dotnet: { label: ".NET", category: "runtime" },
  rust: { label: "Rust", category: "runtime" },
  elixir: { label: "Elixir", category: "runtime" },
  docker: { label: "Docker", category: "platform" },
};

/** Design tokens a project or tech icon may be tinted with (daemon `color_token`). */
export type IconColorToken =
  | "chart-1"
  | "chart-2"
  | "chart-3"
  | "chart-4"
  | "chart-5"
  | "role-proxy"
  | "role-app"
  | "role-db"
  | "role-cache"
  | "role-worker"
  | "primary";

/** Tile surface, ring and glyph colour per token. Full class names so Tailwind finds them. */
export const ICON_TONE_CLASSES: Record<IconColorToken, string> = {
  "chart-1": "bg-chart-1/14 text-chart-1 ring-chart-1/25",
  "chart-2": "bg-chart-2/16 text-chart-2 ring-chart-2/30",
  "chart-3": "bg-chart-3/14 text-chart-3 ring-chart-3/25",
  "chart-4": "bg-chart-4/18 text-chart-4 ring-chart-4/35",
  "chart-5": "bg-chart-5/14 text-chart-5 ring-chart-5/25",
  "role-proxy": "bg-role-proxy/14 text-role-proxy ring-role-proxy/25",
  "role-app": "bg-role-app/14 text-role-app ring-role-app/25",
  "role-db": "bg-role-db/14 text-role-db ring-role-db/25",
  "role-cache": "bg-role-cache/16 text-role-cache ring-role-cache/30",
  "role-worker": "bg-role-worker/14 text-role-worker ring-role-worker/25",
  primary: "bg-primary/14 text-primary ring-primary/25",
};

export const CATEGORY_COLOR: Record<TechCategory, IconColorToken> = {
  app: "chart-1",
  runtime: "role-app",
  monitoring: "chart-2",
  database: "role-db",
  queue: "role-worker",
  storage: "chart-3",
  cache: "role-cache",
  proxy: "role-proxy",
  platform: "chart-4",
};

export function isIconColorToken(value: string): value is IconColorToken {
  return Object.prototype.hasOwnProperty.call(ICON_TONE_CLASSES, value);
}

export function techMeta(slug: string | undefined): TechMeta | undefined {
  if (!slug) return undefined;
  return Object.prototype.hasOwnProperty.call(TECH, slug) ? TECH[slug] : undefined;
}

/** Category to fall back on when the image is a custom build (no tech slug). */
export function roleCategory(role: WorkloadRole | undefined): TechCategory {
  switch (role) {
    case WorkloadRole.PROXY:
      return "proxy";
    case WorkloadRole.DATABASE:
      return "database";
    case WorkloadRole.CACHE:
      return "cache";
    case WorkloadRole.QUEUE:
      return "queue";
    case WorkloadRole.STORAGE:
      return "storage";
    case WorkloadRole.WORKER:
      return "queue";
    default:
      return "runtime";
  }
}

/** Client copy of the daemon's monogram rule, for projects without an icon yet. */
export function monogramFor(name: string): string {
  const words = name.split(/[^\p{L}\p{N}]+/u).filter(Boolean);
  if (words.length === 0) return "?";
  if (words.length === 1) return Array.from(words[0]).slice(0, 2).join("").toUpperCase();
  return (Array.from(words[0])[0] + Array.from(words[1])[0]).toUpperCase();
}
