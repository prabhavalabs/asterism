package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config holds runtime configuration for the Asterism daemon.
type Config struct {
	Server  ServerConfig  `toml:"server"`
	Collect CollectConfig `toml:"collect"`
	Auth    AuthConfig    `toml:"auth"`
	Actions ActionsConfig `toml:"actions"`
	Alerts  AlertsConfig  `toml:"alerts"`
	// Projects holds optional per-project display settings keyed by project name,
	// e.g. [projects."shop"] icon = "postgres".
	Projects ProjectConfigs `toml:"projects"`
	// FaviconFetch is [projects] favicon_fetch (default true; env
	// ASTERISM_FAVICON_FETCH): whether the hub may fetch favicons over https
	// from hostnames routed to a project. It shares the [projects] table with
	// the per-project entries, so it is decoded separately.
	FaviconFetch bool `toml:"-"`
	// Agents are the enrolled asterism-agent hosts ([[agents]]), used when
	// collect.mode = "agent". Generate entries with `asterism agent token create`.
	Agents []AgentConfig `toml:"agents"`
}

// AgentConfig enrolls one asterism-agent. Only the token digest is stored.
type AgentConfig struct {
	// Name is the host id the token is bound to (the agent's host.name).
	Name string `toml:"name"`
	// SHA256 is the lowercase hex SHA-256 digest of the agent token.
	SHA256 string `toml:"sha256"`
	// MachineID optionally pins the agent's machine_id_hash; empty accepts any
	// machine and logs the observed value on first connect.
	MachineID string `toml:"machine_id"`
	// AllowActions is the hub-side per-agent action gate. Actions also need
	// [actions] allow_actions, the caller's actions scope, and the agent's own
	// agent.yaml actions allow-list.
	AllowActions bool `toml:"allow_actions"`
}

// AgentCollectConfig tunes agent ingest ([collect.agent]).
type AgentCollectConfig struct {
	// PrimaryHost is the agent whose data the single-host UI shows. Empty
	// selects the only enrolled agent (or the first one, with a warning).
	PrimaryHost string `toml:"primary_host"`
	// StaleAfter is how long an agent may be silent before it is shown as
	// offline and an agent.offline event is raised (default 30s).
	StaleAfter time.Duration `toml:"stale_after"`
}

// PrimaryAgent returns the primary agent name ("" when no agents are enrolled).
func (c Config) PrimaryAgent() string {
	if c.Collect.Agent.PrimaryHost != "" {
		return c.Collect.Agent.PrimaryHost
	}
	if len(c.Agents) > 0 {
		return c.Agents[0].Name
	}
	return ""
}

// ProjectConfig customises how a project is presented in the UI.
type ProjectConfig struct {
	// Icon is a technology slug such as "postgres", "nginx", or "grafana".
	// Unknown slugs are ignored and the icon is detected instead.
	Icon string `toml:"icon"`
	// Color is a design-token name such as "chart-3" or "role-db".
	Color string `toml:"color"`
	// FaviconFetch disables (false) favicon fetching from this project's
	// public hostnames; nil follows [projects] favicon_fetch.
	FaviconFetch *bool `toml:"favicon_fetch"`
}

// ProjectConfigs is the [projects] table: per-project sub-tables keyed by
// project name, plus the scalar favicon_fetch switch (read into
// Config.FaviconFetch).
type ProjectConfigs map[string]ProjectConfig

// reservedProjectKeys are scalar [projects] settings, not project names.
var reservedProjectKeys = map[string]bool{"favicon_fetch": true}

// UnmarshalTOML decodes the per-project sub-tables and skips the reserved
// scalar keys.
func (p *ProjectConfigs) UnmarshalTOML(v any) error {
	table, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("[projects] must be a table")
	}
	out := make(ProjectConfigs, len(table))
	for name, raw := range table {
		if reservedProjectKeys[name] {
			continue
		}
		fields, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("[projects.%q] must be a table", name)
		}
		var pc ProjectConfig
		for k, fv := range fields {
			switch k {
			case "icon", "color":
				str, ok := fv.(string)
				if !ok {
					return fmt.Errorf("[projects.%q] %s must be a string", name, k)
				}
				if k == "icon" {
					pc.Icon = str
				} else {
					pc.Color = str
				}
			case "favicon_fetch":
				b, ok := fv.(bool)
				if !ok {
					return fmt.Errorf("[projects.%q] favicon_fetch must be true or false", name)
				}
				pc.FaviconFetch = &b
			default:
				return fmt.Errorf("[projects.%q] has unknown key %q (want icon, color or favicon_fetch)", name, k)
			}
		}
		out[name] = pc
	}
	*p = out
	return nil
}

// FaviconFetchFor reports whether favicons may be fetched from the public
// hostnames of the named project.
func (c Config) FaviconFetchFor(project string) bool {
	if pc, ok := c.Projects[project]; ok && pc.FaviconFetch != nil {
		return c.FaviconFetch && *pc.FaviconFetch
	}
	return c.FaviconFetch
}

type ServerConfig struct {
	Listen    string `toml:"listen"`
	BehindTLS bool   `toml:"behind_tls"`
	// PublicURL is the external origin, e.g. "https://monitor.example.com".
	// It makes link-preview tags (og:url, og:image) absolute; empty keeps them
	// relative, which most preview crawlers ignore.
	PublicURL string `toml:"public_url"`
	// DataDir holds Asterism's own state: the SQLite store, secret.key,
	// vapid.json and project icons. Relative paths resolve against the
	// working directory. Env ASTERISM_DATA_DIR; production uses
	// /var/lib/asterism (systemd StateDirectory=asterism).
	DataDir string `toml:"data_dir"`
}

type CollectConfig struct {
	Mode      string `toml:"mode"` // "local", "showcase", "ssh", "agent"
	SSHTarget string `toml:"ssh_target"`
	// Agent configures collect.mode = "agent".
	Agent AgentCollectConfig `toml:"agent"`
	// ShowcaseFleet adds this many generated compose projects to showcase mode
	// (0 keeps only the curated demo topology; capped at 40).
	ShowcaseFleet int `toml:"showcase_fleet"`
}

type AuthConfig struct {
	AdminEmail      string           `toml:"admin_email"`
	AdminPassword   string           `toml:"admin_password"`
	PasswordHash    string           `toml:"password_hash"`
	SessionTTLHours int              `toml:"session_ttl_hours"`
	Tokens          []APITokenConfig `toml:"tokens"`
}

// APITokenConfig is a bearer token for CLI/MCP/API clients. Only the SHA-256
// digest of the token is stored; generate one with `asterism token generate`.
type APITokenConfig struct {
	Name   string   `toml:"name"`
	SHA256 string   `toml:"sha256"`
	Scopes []string `toml:"scopes"`
}

// ActionsConfig gates every mutating code path. All fields default to the safe value.
type ActionsConfig struct {
	// AllowActions enables container start/stop/restart/pause/unpause. When false
	// every action RPC returns FailedPrecondition and the UI hides action buttons.
	AllowActions bool `toml:"allow_actions"`
	// ProtectedContainers lists container names that actions must never touch
	// (in addition to containers labelled asterism.protected=true or asterism.self=true
	// and containers running the asterism image).
	ProtectedContainers []string `toml:"protected_containers"`
	// AllowProtected lifts the protection above. Leave false unless you really
	// want Asterism to be able to stop itself.
	AllowProtected bool `toml:"allow_protected"`
	// DefaultStopTimeoutSeconds is used by stop/restart when the caller omits a timeout.
	DefaultStopTimeoutSeconds int `toml:"default_stop_timeout_seconds"`
}

// AlertsConfig configures hub-side alert evaluation and notifications ([alerts]).
type AlertsConfig struct {
	// Enabled turns rule evaluation and notification delivery on (default true).
	Enabled bool `toml:"enabled"`
	// EvaluationInterval is how often rules are evaluated (default 15s, 5s to 5m).
	EvaluationInterval time.Duration `toml:"evaluation_interval"`
	// GroupWait batches alerts of one project that fire within this window
	// into one notification (default 30s, 0 to 5m).
	GroupWait time.Duration `toml:"group_wait"`
	// RepeatInterval re-notifies alerts that are still firing (default 4h).
	RepeatInterval time.Duration `toml:"repeat_interval"`
	// AllowPrivateTargets lets HTTP channels (Slack, webhook, Discord,
	// Telegram, Web Push) reach loopback, private, and link-local addresses,
	// and plain http on them. Leave false unless the receiver is on your LAN.
	AllowPrivateTargets bool `toml:"allow_private_targets"`
}

// StopTimeout returns the default graceful stop timeout.
func (a ActionsConfig) StopTimeout() time.Duration {
	if a.DefaultStopTimeoutSeconds <= 0 {
		return 10 * time.Second
	}
	if a.DefaultStopTimeoutSeconds > 300 {
		return 300 * time.Second
	}
	return time.Duration(a.DefaultStopTimeoutSeconds) * time.Second
}

// Default returns safe read-only defaults bound to loopback.
func Default() Config {
	return Config{
		Server: ServerConfig{
			Listen:    "127.0.0.1:7700",
			BehindTLS: false,
			DataDir:   "data",
		},
		Collect: CollectConfig{
			Mode:  "local",
			Agent: AgentCollectConfig{StaleAfter: 30 * time.Second},
		},
		Auth: AuthConfig{
			SessionTTLHours: 24,
		},
		Actions: ActionsConfig{
			AllowActions:              false,
			DefaultStopTimeoutSeconds: 10,
		},
		FaviconFetch: true,
		Alerts: AlertsConfig{
			Enabled:            true,
			EvaluationInterval: 15 * time.Second,
			GroupWait:          30 * time.Second,
			RepeatInterval:     4 * time.Hour,
		},
	}
}

// EnsureDataDir resolves server.data_dir to an absolute path and creates it
// with mode 0700 when missing.
func (c Config) EnsureDataDir() (string, error) {
	dir := strings.TrimSpace(c.Server.DataDir)
	if dir == "" {
		dir = "data"
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve data_dir %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("create data_dir %s: %w", abs, err)
	}
	return abs, nil
}

// Load reads configuration from the given path (or asterism.local.toml / asterism.toml
// if path is empty) and applies ASTERISM_* environment variable overrides.
func Load(path string) (Config, string, error) {
	cfg := Default()
	resolvedPath := path

	if resolvedPath == "" {
		for _, candidate := range []string{"asterism.local.toml", "asterism.toml", "/etc/asterism/asterism.toml"} {
			if _, err := os.Stat(candidate); err == nil {
				resolvedPath = candidate
				break
			}
		}
	}

	if resolvedPath != "" {
		clean := filepath.Clean(resolvedPath)
		if _, err := toml.DecodeFile(clean, &cfg); err != nil {
			return Config{}, resolvedPath, fmt.Errorf("decode config %s: %w", clean, err)
		}
		// [projects] favicon_fetch lives next to the per-project tables.
		var switches struct {
			Projects struct {
				FaviconFetch *bool `toml:"favicon_fetch"`
			} `toml:"projects"`
		}
		if _, err := toml.DecodeFile(clean, &switches); err != nil {
			return Config{}, resolvedPath, fmt.Errorf("decode config %s: %w", clean, err)
		}
		if switches.Projects.FaviconFetch != nil {
			cfg.FaviconFetch = *switches.Projects.FaviconFetch
		}
	}

	applyEnv(&cfg)

	if cfg.Server.Listen == "" {
		cfg.Server.Listen = "127.0.0.1:7700"
	}
	if cfg.Collect.Mode == "" {
		cfg.Collect.Mode = "local"
	}
	if cfg.Auth.SessionTTLHours <= 0 {
		cfg.Auth.SessionTTLHours = 24
	}

	switch cfg.Collect.Mode {
	case "local", "showcase", "ssh", "agent":
	default:
		return Config{}, resolvedPath, fmt.Errorf("invalid collect.mode %q (want local, showcase, ssh, or agent)", cfg.Collect.Mode)
	}
	if err := cfg.validateAgents(); err != nil {
		return Config{}, resolvedPath, err
	}
	publicURL, err := normalizePublicURL(cfg.Server.PublicURL)
	if err != nil {
		return Config{}, resolvedPath, err
	}
	cfg.Server.PublicURL = publicURL
	if err := cfg.validateAlerts(); err != nil {
		return Config{}, resolvedPath, err
	}

	return cfg, resolvedPath, nil
}

const (
	minStaleAfter = 5 * time.Second
	maxStaleAfter = 10 * time.Minute
)

// validateAgents checks [[agents]] and [collect.agent]. Token digests are
// validated again by auth.NewManager.
func (cfg *Config) validateAgents() error {
	a := &cfg.Collect.Agent
	switch {
	case a.StaleAfter == 0:
		a.StaleAfter = 30 * time.Second
	case a.StaleAfter < minStaleAfter || a.StaleAfter > maxStaleAfter:
		return fmt.Errorf("collect.agent.stale_after = %s is out of range (%s to %s; agents heartbeat every 10s)", a.StaleAfter, minStaleAfter, maxStaleAfter)
	}
	seen := make(map[string]bool, len(cfg.Agents))
	for i, ag := range cfg.Agents {
		name := strings.TrimSpace(ag.Name)
		if name == "" {
			return fmt.Errorf("[[agents]] entry %d has no name", i+1)
		}
		if seen[name] {
			return fmt.Errorf("[[agents]] name %q is enrolled twice", name)
		}
		seen[name] = true
		cfg.Agents[i].Name = name
	}
	if a.PrimaryHost != "" && !seen[a.PrimaryHost] {
		return fmt.Errorf("collect.agent.primary_host = %q is not an enrolled [[agents]] name", a.PrimaryHost)
	}
	if cfg.Collect.Mode == "agent" && len(cfg.Agents) == 0 {
		return fmt.Errorf("collect.mode = \"agent\" needs at least one [[agents]] entry; create one with `asterism agent token create --host <name>`")
	}
	return nil
}

// validateAlerts fills zero durations with defaults and checks ranges.
func (cfg *Config) validateAlerts() error {
	a := &cfg.Alerts
	def := Default().Alerts
	if a.EvaluationInterval == 0 {
		a.EvaluationInterval = def.EvaluationInterval
	}
	if a.EvaluationInterval < 5*time.Second || a.EvaluationInterval > 5*time.Minute {
		return fmt.Errorf("alerts.evaluation_interval = %s is out of range (5s to 5m)", a.EvaluationInterval)
	}
	if a.GroupWait < 0 || a.GroupWait > 5*time.Minute {
		return fmt.Errorf("alerts.group_wait = %s is out of range (0 to 5m)", a.GroupWait)
	}
	if a.RepeatInterval == 0 {
		a.RepeatInterval = def.RepeatInterval
	}
	if a.RepeatInterval < 5*time.Minute || a.RepeatInterval > 7*24*time.Hour {
		return fmt.Errorf("alerts.repeat_interval = %s is out of range (5m to 168h)", a.RepeatInterval)
	}
	return nil
}

func (c AuthConfig) SessionTTL() time.Duration {
	if c.SessionTTLHours <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(c.SessionTTLHours) * time.Hour
}

func applyEnv(cfg *Config) {
	if v := strings.TrimSpace(os.Getenv("ASTERISM_LISTEN")); v != "" {
		cfg.Server.Listen = v
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_BEHIND_TLS")); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Server.BehindTLS = b
		}
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_PUBLIC_URL")); v != "" {
		cfg.Server.PublicURL = v
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_DATA_DIR")); v != "" {
		cfg.Server.DataDir = v
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_FAVICON_FETCH")); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.FaviconFetch = b
		}
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_ALERTS_ENABLED")); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Alerts.Enabled = b
		}
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_ALERTS_EVALUATION_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Alerts.EvaluationInterval = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_ALERTS_GROUP_WAIT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Alerts.GroupWait = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_ALERTS_ALLOW_PRIVATE_TARGETS")); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Alerts.AllowPrivateTargets = b
		}
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_COLLECT_MODE")); v != "" {
		cfg.Collect.Mode = strings.ToLower(v)
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_SSH_TARGET")); v != "" {
		cfg.Collect.SSHTarget = v
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_SHOWCASE_FLEET")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.Collect.ShowcaseFleet = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_ADMIN_EMAIL")); v != "" {
		cfg.Auth.AdminEmail = v
	}
	if v := os.Getenv("ASTERISM_ADMIN_PASSWORD"); v != "" {
		cfg.Auth.AdminPassword = v
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_ADMIN_PASSWORD_HASH")); v != "" {
		cfg.Auth.PasswordHash = v
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_SESSION_TTL_HOURS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Auth.SessionTTLHours = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("ASTERISM_ALLOW_ACTIONS")); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Actions.AllowActions = b
		}
	}
}

// normalizePublicURL accepts an absolute http(s) origin and returns it without
// a trailing slash. Paths, queries and fragments are rejected so the value can
// be spliced into HTML attributes safely.
func normalizePublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		u.User != nil || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" ||
		strings.ContainsAny(u.Host, "\"'<> ") {
		return "", fmt.Errorf("server.public_url = %q must be an origin such as https://monitor.example.com", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}
