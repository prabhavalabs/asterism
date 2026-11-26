package config

import "time"

// DefaultPath is where the agent looks for its configuration.
const DefaultPath = "/etc/asterism/agent.yaml"

// DefaultTokenFile is the default hub.token_file.
const DefaultTokenFile = "/etc/asterism/agent.token"

// Default returns the configuration with every documented default applied.
func Default() *Config {
	return &Config{
		Version: 1,
		Hub: Hub{
			TokenFile:   DefaultTokenFile,
			Transport:   "auto",
			Compression: "gzip",
			TLS:         TLS{PinSHA256: []string{}},
			Buffer:      Buffer{MaxBytes: 1 << 20, MaxEvents: 500},
			Backoff:     Backoff{Initial: Duration(time.Second), Max: Duration(60 * time.Second)},
		},
		Host: Host{
			Labels:      map[string]string{},
			Root:        "/",
			Filesystems: []string{"/"},
			Interfaces: Interfaces{Exclude: []string{
				"lo", "veth*", "br-*", "docker0", "cni*", "flannel*",
			}},
		},
		Intervals: Intervals{
			Host:            Duration(2 * time.Second),
			Containers:      Duration(5 * time.Second),
			Listeners:       Duration(15 * time.Second),
			Units:           Duration(30 * time.Second),
			Processes:       Duration(30 * time.Second),
			Proxies:         Duration(30 * time.Second),
			DockerResources: Duration(5 * time.Minute),
			DockerDiskUsage: Duration(time.Hour),
			InventoryResync: Duration(5 * time.Minute),
			Keyframe:        Duration(60 * time.Second),
			Flush:           Duration(5 * time.Second),
			Adaptive:        true,
			IdleFactor:      3,
			Min:             Duration(time.Second),
			Max:             Duration(60 * time.Second),
		},
		Sources: Sources{
			Docker: Docker{
				Enabled:  TriAuto,
				Endpoint: "unix:///var/run/docker.sock",
				Include:  Selector{Names: []string{}, Labels: map[string]string{}},
				Exclude: Selector{
					Names:  []string{"buildx_buildkit_*"},
					Labels: map[string]string{"asterism.ignore": "true"},
				},
				GroupBy: "compose",
				Stats:   "cgroup",
			},
			Podman: Podman{
				Enabled:  TriFalse,
				Endpoint: "unix:///run/podman/podman.sock",
			},
			Systemd: Systemd{
				Enabled:  TriAuto,
				Discover: "listening",
				Include: []string{
					"nginx.service", "caddy.service", "haproxy.service", "cloudflared*.service",
					"postgresql*.service", "mysql.service", "mariadb.service", "redis*.service",
				},
				Exclude: []string{"systemd-*", "dbus*", "getty*", "user@*"},
				DBus:    TriAuto,
			},
			Processes: Processes{Enabled: true, Match: []ProcessMatch{}},
			PM2:       PM2{Enabled: TriAuto, Homes: []string{}},
			Proxies: Proxies{
				Nginx:       Nginx{Enabled: TriAuto, Config: "/etc/nginx/nginx.conf"},
				Caddy:       Caddy{Enabled: TriAuto, AdminAPI: "http://127.0.0.1:2019", Caddyfile: "/etc/caddy/Caddyfile"},
				Traefik:     Traefik{Enabled: TriAuto, DockerLabels: true},
				HAProxy:     HAProxy{Enabled: TriAuto, Config: "/etc/haproxy/haproxy.cfg"},
				Cloudflared: Cloudflared{Enabled: TriAuto, Config: "/etc/cloudflared/config.yml"},
			},
			Ports:  Ports{Enabled: true, Protocols: []string{"tcp", "udp"}, ResolveOwners: true},
			Routes: []Route{},
			Logs: Logs{
				Docker:    true,
				Journald:  true,
				Files:     []LogFile{},
				MaxLines:  500,
				MaxFollow: Duration(10 * time.Minute),
				RateLimit: 64 << 10,
			},
		},
		Projects:  []Project{},
		Redaction: Redaction{ExtraSecretKeys: []string{}, DropLabels: []string{}},
		Actions:   Actions{Allow: []string{}, Containers: []string{}, Protect: []string{}},
		Agent: Agent{
			LogLevel:    "info",
			LogFormat:   "text",
			GOMAXPROCS:  1,
			MemoryLimit: 12 << 20,
			GCPercent:   50,
		},
	}
}
