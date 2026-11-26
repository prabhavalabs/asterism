package service

import (
	"strings"
)

// TechCategory groups technologies for icon colour and project dominance.
type TechCategory string

const (
	TechApp        TechCategory = "app"
	TechRuntime    TechCategory = "runtime"
	TechMonitoring TechCategory = "monitoring"
	TechDatabase   TechCategory = "database"
	TechQueue      TechCategory = "queue"
	TechStorage    TechCategory = "storage"
	TechCache      TechCategory = "cache"
	TechProxy      TechCategory = "proxy"
	TechPlatform   TechCategory = "platform"
)

// categoryPriority ranks how strongly a technology defines a project: a
// WordPress or Grafana container says more about a stack than its Redis.
var categoryPriority = map[TechCategory]int{
	TechApp:        8,
	TechMonitoring: 7,
	TechRuntime:    6,
	TechDatabase:   5,
	TechQueue:      4,
	TechStorage:    3,
	TechProxy:      2,
	TechCache:      1,
	TechPlatform:   0,
}

// categoryColor is the default design token for icons of each category.
var categoryColor = map[TechCategory]string{
	TechApp:        "chart-1",
	TechRuntime:    "role-app",
	TechMonitoring: "chart-2",
	TechDatabase:   "role-db",
	TechQueue:      "role-worker",
	TechStorage:    "chart-3",
	TechProxy:      "role-proxy",
	TechCache:      "role-cache",
	TechPlatform:   "chart-4",
}

// Tech describes one recognisable technology. The web UI keeps a matching
// slug → glyph table in web/src/components/common/tech.ts.
type Tech struct {
	Slug     string
	Label    string
	Category TechCategory
	// Names are image repository names (last path segment) that identify the tech,
	// exactly or as a prefix followed by "-", "_" or ".".
	Names []string
	// Orgs are registry namespaces whose images all belong to the tech (e.g. "grafana/loki").
	Orgs []string
}

// Color returns the default design-token colour for the tech.
func (t Tech) Color() string { return categoryColor[t.Category] }

// techCatalog is ordered: for prefix matches the first entry wins, so more
// specific names (timescaledb, node-red) come before generic ones (postgres, node).
var techCatalog = []Tech{
	// Web apps and self-hosted tools.
	{Slug: "wordpress", Label: "WordPress", Category: TechApp, Names: []string{"wordpress"}},
	{Slug: "ghost", Label: "Ghost", Category: TechApp, Names: []string{"ghost"}},
	{Slug: "nextcloud", Label: "Nextcloud", Category: TechApp, Names: []string{"nextcloud"}},
	{Slug: "gitea", Label: "Gitea", Category: TechApp, Names: []string{"gitea"}, Orgs: []string{"gitea"}},
	{Slug: "forgejo", Label: "Forgejo", Category: TechApp, Names: []string{"forgejo"}},
	{Slug: "gitlab", Label: "GitLab", Category: TechApp, Names: []string{"gitlab-ce", "gitlab-ee", "gitlab-runner", "gitlab"}},
	{Slug: "n8n", Label: "n8n", Category: TechApp, Names: []string{"n8n"}, Orgs: []string{"n8nio"}},
	{Slug: "nodered", Label: "Node-RED", Category: TechApp, Names: []string{"node-red"}, Orgs: []string{"nodered"}},
	{Slug: "umami", Label: "Umami", Category: TechApp, Names: []string{"umami"}},
	{Slug: "plausible", Label: "Plausible", Category: TechApp, Names: []string{"plausible"}, Orgs: []string{"plausible"}},
	{Slug: "vaultwarden", Label: "Vaultwarden", Category: TechApp, Names: []string{"vaultwarden"}, Orgs: []string{"vaultwarden"}},
	{Slug: "keycloak", Label: "Keycloak", Category: TechApp, Names: []string{"keycloak"}},
	{Slug: "mattermost", Label: "Mattermost", Category: TechApp, Names: []string{"mattermost-team-edition", "mattermost-enterprise-edition", "mattermost"}},
	{Slug: "metabase", Label: "Metabase", Category: TechApp, Names: []string{"metabase"}},
	{Slug: "directus", Label: "Directus", Category: TechApp, Names: []string{"directus"}},
	{Slug: "strapi", Label: "Strapi", Category: TechApp, Names: []string{"strapi"}},
	{Slug: "supabase", Label: "Supabase", Category: TechApp, Orgs: []string{"supabase"}},
	{Slug: "appwrite", Label: "Appwrite", Category: TechApp, Names: []string{"appwrite"}, Orgs: []string{"appwrite"}},
	{Slug: "pocketbase", Label: "PocketBase", Category: TechApp, Names: []string{"pocketbase"}},
	{Slug: "hasura", Label: "Hasura", Category: TechApp, Names: []string{"graphql-engine"}, Orgs: []string{"hasura"}},
	{Slug: "immich", Label: "Immich", Category: TechApp, Names: []string{"immich-server", "immich-machine-learning", "immich"}},
	{Slug: "jellyfin", Label: "Jellyfin", Category: TechApp, Names: []string{"jellyfin"}},
	{Slug: "homeassistant", Label: "Home Assistant", Category: TechApp, Names: []string{"home-assistant", "homeassistant"}},
	{Slug: "ollama", Label: "Ollama", Category: TechApp, Names: []string{"ollama"}},
	{Slug: "mastodon", Label: "Mastodon", Category: TechApp, Names: []string{"mastodon"}},
	{Slug: "discourse", Label: "Discourse", Category: TechApp, Names: []string{"discourse"}},
	{Slug: "listmonk", Label: "listmonk", Category: TechApp, Names: []string{"listmonk"}},
	{Slug: "ntfy", Label: "ntfy", Category: TechApp, Names: []string{"ntfy"}},
	{Slug: "pihole", Label: "Pi-hole", Category: TechApp, Names: []string{"pihole"}},
	{Slug: "adguard", Label: "AdGuard Home", Category: TechApp, Names: []string{"adguardhome"}},
	{Slug: "syncthing", Label: "Syncthing", Category: TechApp, Names: []string{"syncthing"}},
	{Slug: "jenkins", Label: "Jenkins", Category: TechApp, Names: []string{"jenkins"}},
	{Slug: "drone", Label: "Drone", Category: TechApp, Names: []string{"drone", "drone-runner-docker"}},
	{Slug: "portainer", Label: "Portainer", Category: TechApp, Names: []string{"portainer-ce", "portainer-ee", "portainer"}, Orgs: []string{"portainer"}},
	// Monitoring and observability.
	{Slug: "grafana", Label: "Grafana", Category: TechMonitoring, Names: []string{"grafana", "grafana-oss", "grafana-enterprise", "loki", "promtail", "tempo", "alloy"}, Orgs: []string{"grafana"}},
	{Slug: "prometheus", Label: "Prometheus", Category: TechMonitoring, Names: []string{"prometheus", "alertmanager", "pushgateway", "node-exporter", "postgres-exporter", "redis_exporter", "cadvisor"}, Orgs: []string{"prom"}},
	{Slug: "victoriametrics", Label: "VictoriaMetrics", Category: TechMonitoring, Names: []string{"victoria-metrics", "vmagent", "vmalert", "victoria-logs"}, Orgs: []string{"victoriametrics"}},
	{Slug: "uptimekuma", Label: "Uptime Kuma", Category: TechMonitoring, Names: []string{"uptime-kuma"}},
	{Slug: "sentry", Label: "Sentry", Category: TechMonitoring, Names: []string{"sentry"}, Orgs: []string{"getsentry"}},
	{Slug: "opentelemetry", Label: "OpenTelemetry", Category: TechMonitoring, Names: []string{"opentelemetry-collector", "opentelemetry-collector-contrib", "jaeger"}, Orgs: []string{"otel", "jaegertracing"}},
	// Databases and search.
	{Slug: "timescale", Label: "TimescaleDB", Category: TechDatabase, Names: []string{"timescaledb", "timescaledb-ha"}, Orgs: []string{"timescale"}},
	{Slug: "postgres", Label: "PostgreSQL", Category: TechDatabase, Names: []string{"postgres", "postgresql", "postgis", "pgvector", "pgbouncer", "cloudnative-pg"}},
	{Slug: "mysql", Label: "MySQL", Category: TechDatabase, Names: []string{"mysql", "mysql-server", "percona-server", "percona"}},
	{Slug: "mariadb", Label: "MariaDB", Category: TechDatabase, Names: []string{"mariadb"}},
	{Slug: "mongodb", Label: "MongoDB", Category: TechDatabase, Names: []string{"mongo", "mongodb", "mongodb-community-server"}},
	{Slug: "clickhouse", Label: "ClickHouse", Category: TechDatabase, Names: []string{"clickhouse-server", "clickhouse"}},
	{Slug: "influxdb", Label: "InfluxDB", Category: TechDatabase, Names: []string{"influxdb"}},
	{Slug: "elasticsearch", Label: "Elasticsearch", Category: TechDatabase, Names: []string{"elasticsearch", "kibana", "logstash"}},
	{Slug: "opensearch", Label: "OpenSearch", Category: TechDatabase, Names: []string{"opensearch", "opensearch-dashboards"}},
	{Slug: "meilisearch", Label: "Meilisearch", Category: TechDatabase, Names: []string{"meilisearch"}},
	{Slug: "neo4j", Label: "Neo4j", Category: TechDatabase, Names: []string{"neo4j"}},
	{Slug: "cassandra", Label: "Cassandra", Category: TechDatabase, Names: []string{"cassandra", "scylla"}},
	{Slug: "etcd", Label: "etcd", Category: TechDatabase, Names: []string{"etcd"}},
	// Queues and brokers.
	{Slug: "rabbitmq", Label: "RabbitMQ", Category: TechQueue, Names: []string{"rabbitmq"}},
	{Slug: "nats", Label: "NATS", Category: TechQueue, Names: []string{"nats", "nats-streaming"}},
	{Slug: "kafka", Label: "Apache Kafka", Category: TechQueue, Names: []string{"kafka", "cp-kafka", "redpanda"}},
	// Object storage and backups.
	{Slug: "minio", Label: "MinIO", Category: TechStorage, Names: []string{"minio"}},
	{Slug: "garage", Label: "Garage", Category: TechStorage, Names: []string{"garage"}, Orgs: []string{"dxflrs"}},
	{Slug: "restic", Label: "restic", Category: TechStorage, Names: []string{"restic", "rest-server", "backrest"}},
	{Slug: "rclone", Label: "rclone", Category: TechStorage, Names: []string{"rclone"}},
	// Caches.
	{Slug: "valkey", Label: "Valkey", Category: TechCache, Names: []string{"valkey", "valkey-bundle"}},
	{Slug: "redis", Label: "Redis", Category: TechCache, Names: []string{"redis", "redis-stack", "redis-stack-server", "keydb", "dragonfly"}},
	{Slug: "memcached", Label: "Memcached", Category: TechCache, Names: []string{"memcached"}},
	// Reverse proxies, load balancers and tunnels.
	{Slug: "traefik", Label: "Traefik", Category: TechProxy, Names: []string{"traefik"}},
	{Slug: "caddy", Label: "Caddy", Category: TechProxy, Names: []string{"caddy"}},
	{Slug: "cloudflared", Label: "Cloudflare Tunnel", Category: TechProxy, Names: []string{"cloudflared"}},
	{Slug: "haproxy", Label: "HAProxy", Category: TechProxy, Names: []string{"haproxy"}},
	{Slug: "envoy", Label: "Envoy", Category: TechProxy, Names: []string{"envoy"}, Orgs: []string{"envoyproxy"}},
	{Slug: "nginx", Label: "Nginx", Category: TechProxy, Names: []string{"nginx", "openresty", "nginx-proxy", "nginx-proxy-manager", "nginx-unprivileged", "angie"}},
	{Slug: "apache", Label: "Apache httpd", Category: TechProxy, Names: []string{"httpd", "apache2"}},
	{Slug: "tailscale", Label: "Tailscale", Category: TechProxy, Names: []string{"tailscale"}},
	{Slug: "wireguard", Label: "WireGuard", Category: TechProxy, Names: []string{"wireguard", "wg-easy"}},
	// Language runtimes (generic base images).
	{Slug: "node", Label: "Node.js", Category: TechRuntime, Names: []string{"node", "nodejs"}},
	{Slug: "bun", Label: "Bun", Category: TechRuntime, Names: []string{"bun"}, Orgs: []string{"oven"}},
	{Slug: "deno", Label: "Deno", Category: TechRuntime, Names: []string{"deno"}, Orgs: []string{"denoland"}},
	{Slug: "python", Label: "Python", Category: TechRuntime, Names: []string{"python", "pypy"}},
	{Slug: "go", Label: "Go", Category: TechRuntime, Names: []string{"golang"}},
	{Slug: "php", Label: "PHP", Category: TechRuntime, Names: []string{"php", "php-fpm", "frankenphp"}},
	{Slug: "ruby", Label: "Ruby", Category: TechRuntime, Names: []string{"ruby"}},
	{Slug: "java", Label: "Java", Category: TechRuntime, Names: []string{"openjdk", "eclipse-temurin", "amazoncorretto", "tomcat"}},
	{Slug: "dotnet", Label: ".NET", Category: TechRuntime, Orgs: []string{"dotnet"}},
	{Slug: "rust", Label: "Rust", Category: TechRuntime, Names: []string{"rust"}},
	{Slug: "elixir", Label: "Elixir", Category: TechRuntime, Names: []string{"elixir"}},
	// Container platform.
	{Slug: "docker", Label: "Docker", Category: TechPlatform, Names: []string{"docker", "dind", "docker-socket-proxy", "watchtower"}, Orgs: []string{"containrrr"}},
}

var techBySlug = func() map[string]Tech {
	m := make(map[string]Tech, len(techCatalog))
	for _, t := range techCatalog {
		m[t.Slug] = t
	}
	return m
}()

// LookupTech returns the catalogue entry for a slug.
func LookupTech(slug string) (Tech, bool) {
	t, ok := techBySlug[strings.ToLower(strings.TrimSpace(slug))]
	return t, ok
}

// imageParts splits an image reference into its repository name (last path
// segment) and namespace, without registry host, tag or digest. It also
// accepts descriptive strings such as "nginx 1.28.0 (host systemd)".
func imageParts(image string) (name, org string) {
	s := strings.ToLower(strings.TrimSpace(image))
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i]
	}
	if c := strings.LastIndexByte(s, ':'); c > strings.LastIndexByte(s, '/') {
		s = s[:c]
	}
	parts := strings.Split(s, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		parts = parts[1:]
	}
	name = parts[len(parts)-1]
	if len(parts) > 1 {
		org = parts[len(parts)-2]
	}
	return name, org
}

func hasNamePrefix(s, name string) bool {
	if !strings.HasPrefix(s, name) || len(s) == len(name) {
		return false
	}
	switch s[len(name)] {
	case '-', '_', '.':
		return true
	}
	return false
}

// DetectTech returns the technology slug for an image reference, or "" when
// the image is not recognised (custom application images).
func DetectTech(image string) string {
	name, org := imageParts(image)
	if name == "" {
		return ""
	}
	for _, t := range techCatalog {
		for _, n := range t.Names {
			if name == n {
				return t.Slug
			}
		}
	}
	for _, t := range techCatalog {
		for _, o := range t.Orgs {
			if org == o {
				return t.Slug
			}
		}
	}
	if strings.HasSuffix(name, "-exporter") || strings.HasSuffix(name, "_exporter") {
		return "prometheus"
	}
	for _, t := range techCatalog {
		for _, n := range t.Names {
			if hasNamePrefix(name, n) {
				return t.Slug
			}
		}
	}
	return ""
}
