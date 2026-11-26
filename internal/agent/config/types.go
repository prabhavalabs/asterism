// Package config loads, validates and prints the asterism-agent YAML
// configuration (/etc/asterism/agent.yaml).
//
// Every key is optional except hub.url and a token. Unknown keys are errors,
// reported with the file line and the valid keys at that level.
package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is the effective agent configuration.
type Config struct {
	Version   int       `yaml:"version"`
	Hub       Hub       `yaml:"hub"`
	Host      Host      `yaml:"host"`
	Intervals Intervals `yaml:"intervals"`
	Sources   Sources   `yaml:"sources"`
	Projects  []Project `yaml:"projects"`
	Redaction Redaction `yaml:"redaction"`
	Actions   Actions   `yaml:"actions"`
	Agent     Agent     `yaml:"agent"`

	// Path is the file the configuration was loaded from ("" for defaults).
	Path string `yaml:"-"`
	// TokenSource records where the token came from: "token", "token_file",
	// "env:ASTERISM_AGENT_TOKEN" or "env:ASTERISM_AGENT_TOKEN_FILE".
	TokenSource string `yaml:"-"`

	// warnings collected while loading (environment overrides etc.).
	warnings Issues
	// lines maps key paths to YAML lines for validation messages.
	lines map[string]int
	// file is the base name used in issue messages.
	file string
}

// Hub configures the uplink to the hub.
type Hub struct {
	URL         string  `yaml:"url"`
	Token       string  `yaml:"token"`
	TokenFile   string  `yaml:"token_file"`
	Transport   string  `yaml:"transport"`
	Compression string  `yaml:"compression"`
	TLS         TLS     `yaml:"tls"`
	Buffer      Buffer  `yaml:"buffer"`
	Backoff     Backoff `yaml:"backoff"`
}

// TLS configures hub transport security.
type TLS struct {
	CAFile      string   `yaml:"ca_file"`
	ServerName  string   `yaml:"server_name"`
	PinSHA256   []string `yaml:"pin_sha256"`
	ClientCert  string   `yaml:"client_cert"`
	ClientKey   string   `yaml:"client_key"`
	InsecureDev bool     `yaml:"insecure_dev"`
}

// Buffer bounds the in-memory replay rings.
type Buffer struct {
	MaxBytes  ByteSize `yaml:"max_bytes"`
	MaxEvents int      `yaml:"max_events"`
}

// Backoff bounds reconnect delays.
type Backoff struct {
	Initial Duration `yaml:"initial"`
	Max     Duration `yaml:"max"`
}

// Host describes the monitored host.
type Host struct {
	Name        string            `yaml:"name"`
	Labels      map[string]string `yaml:"labels"`
	Root        string            `yaml:"root"`
	Filesystems []string          `yaml:"filesystems"`
	Interfaces  Interfaces        `yaml:"interfaces"`
}

// Interfaces filters network interfaces by glob.
type Interfaces struct {
	Exclude []string `yaml:"exclude"`
}

// Intervals are the collection cadences and adaptive bounds.
type Intervals struct {
	Host            Duration `yaml:"host"`
	Containers      Duration `yaml:"containers"`
	Listeners       Duration `yaml:"listeners"`
	Units           Duration `yaml:"units"`
	Processes       Duration `yaml:"processes"`
	Proxies         Duration `yaml:"proxies"`
	DockerResources Duration `yaml:"docker_resources"`
	DockerDiskUsage Duration `yaml:"docker_disk_usage"`
	InventoryResync Duration `yaml:"inventory_resync"`
	Keyframe        Duration `yaml:"keyframe"`
	Flush           Duration `yaml:"flush"`
	Adaptive        bool     `yaml:"adaptive"`
	IdleFactor      float64  `yaml:"idle_factor"`
	Min             Duration `yaml:"min"`
	Max             Duration `yaml:"max"`
}

// Sources configures each collector family.
type Sources struct {
	Docker    Docker    `yaml:"docker"`
	Podman    Podman    `yaml:"podman"`
	Systemd   Systemd   `yaml:"systemd"`
	Processes Processes `yaml:"processes"`
	PM2       PM2       `yaml:"pm2"`
	Proxies   Proxies   `yaml:"proxies"`
	Ports     Ports     `yaml:"ports"`
	Routes    []Route   `yaml:"routes"`
	Logs      Logs      `yaml:"logs"`
	// ProjectIcons scans compose working directories for favicon files
	// (a fixed list of web-asset folders, no symlinks, 256 KiB cap). Only
	// a digest goes up with the inventory; the hub asks for the bytes once.
	ProjectIcons ProjectIcons `yaml:"project_icons"`
}

// ProjectIcons configures the favicon scan of compose working directories.
type ProjectIcons struct {
	// Enabled: auto (default) and true scan; false never reads project
	// directories.
	Enabled Tri `yaml:"enabled"`
}

// Docker configures the Docker Engine API source.
type Docker struct {
	Enabled  Tri      `yaml:"enabled"`
	Endpoint string   `yaml:"endpoint"`
	Include  Selector `yaml:"include"`
	Exclude  Selector `yaml:"exclude"`
	GroupBy  string   `yaml:"group_by"`
	Stats    string   `yaml:"stats"`
}

// Selector matches containers by name glob and exact label values.
type Selector struct {
	Names  []string          `yaml:"names"`
	Labels map[string]string `yaml:"labels"`
}

// Podman configures the Docker-compatible Podman API source.
type Podman struct {
	Enabled  Tri    `yaml:"enabled"`
	Endpoint string `yaml:"endpoint"`
}

// Systemd configures unit discovery.
type Systemd struct {
	Enabled  Tri      `yaml:"enabled"`
	Discover string   `yaml:"discover"`
	Include  []string `yaml:"include"`
	Exclude  []string `yaml:"exclude"`
	DBus     Tri      `yaml:"dbus"`
}

// Processes configures plain-process matching.
type Processes struct {
	Enabled bool           `yaml:"enabled"`
	Match   []ProcessMatch `yaml:"match"`
}

// ProcessMatch selects processes that become Workload{kind: PROCESS}.
type ProcessMatch struct {
	Name    string `yaml:"name"`
	Exe     string `yaml:"exe"`
	Cmdline string `yaml:"cmdline"`
	User    string `yaml:"user"`
	Role    string `yaml:"role"`
}

// PM2 configures pm2 pid-file discovery.
type PM2 struct {
	Enabled Tri      `yaml:"enabled"`
	Homes   []string `yaml:"homes"`
}

// Proxies configures reverse-proxy config parsing.
type Proxies struct {
	Nginx       Nginx       `yaml:"nginx"`
	Caddy       Caddy       `yaml:"caddy"`
	Traefik     Traefik     `yaml:"traefik"`
	HAProxy     HAProxy     `yaml:"haproxy"`
	Cloudflared Cloudflared `yaml:"cloudflared"`
}

// Nginx configures nginx config parsing.
type Nginx struct {
	Enabled Tri    `yaml:"enabled"`
	Config  string `yaml:"config"`
}

// Caddy configures the Caddy admin API and Caddyfile.
type Caddy struct {
	Enabled   Tri    `yaml:"enabled"`
	AdminAPI  string `yaml:"admin_api"`
	Caddyfile string `yaml:"caddyfile"`
}

// Traefik configures Traefik discovery.
type Traefik struct {
	Enabled      Tri    `yaml:"enabled"`
	API          string `yaml:"api"`
	DockerLabels bool   `yaml:"docker_labels"`
}

// HAProxy configures haproxy.cfg parsing.
type HAProxy struct {
	Enabled Tri    `yaml:"enabled"`
	Config  string `yaml:"config"`
}

// Cloudflared configures cloudflared ingress parsing.
type Cloudflared struct {
	Enabled Tri    `yaml:"enabled"`
	Config  string `yaml:"config"`
}

// Ports configures the listener scan.
type Ports struct {
	Enabled       bool     `yaml:"enabled"`
	Protocols     []string `yaml:"protocols"`
	ResolveOwners bool     `yaml:"resolve_owners"`
}

// Route is a manual IngressRoute, e.g. for a remote-managed tunnel.
type Route struct {
	Proxy      string   `yaml:"proxy"`
	Hostnames  []string `yaml:"hostnames"`
	Upstream   string   `yaml:"upstream"`
	PathPrefix string   `yaml:"path_prefix"`
	ListenPort int      `yaml:"listen_port"`
	TLS        bool     `yaml:"tls"`
}

// Logs configures on-demand log access.
type Logs struct {
	Docker    bool      `yaml:"docker"`
	Journald  bool      `yaml:"journald"`
	Files     []LogFile `yaml:"files"`
	MaxLines  int       `yaml:"max_lines"`
	MaxFollow Duration  `yaml:"max_follow"`
	RateLimit ByteRate  `yaml:"rate_limit"`
}

// LogFile maps a workload to a log file the agent may tail.
type LogFile struct {
	Workload string `yaml:"workload"`
	Path     string `yaml:"path"`
}

// Project is a manual grouping rule resolved on the agent.
type Project struct {
	Name     string  `yaml:"name"`
	Category string  `yaml:"category"`
	Icon     string  `yaml:"icon"`
	Color    string  `yaml:"color"`
	Members  Members `yaml:"members"`
}

// Members lists globs per entity kind.
type Members struct {
	Compose    []string `yaml:"compose"`
	Containers []string `yaml:"containers"`
	Units      []string `yaml:"units"`
	PM2        []string `yaml:"pm2"`
	Processes  []string `yaml:"processes"`
}

// Redaction extends the built-in secret redaction.
type Redaction struct {
	ExtraSecretKeys []string `yaml:"extra_secret_keys"`
	DropLabels      []string `yaml:"drop_labels"`
}

// Actions is the agent-side allow-list for mutating container actions.
type Actions struct {
	Enabled    bool     `yaml:"enabled"`
	Allow      []string `yaml:"allow"`
	Containers []string `yaml:"containers"`
	Protect    []string `yaml:"protect"`
}

// Agent tunes the agent process itself.
type Agent struct {
	LogLevel    string   `yaml:"log_level"`
	LogFormat   string   `yaml:"log_format"`
	GOMAXPROCS  int      `yaml:"gomaxprocs"`
	MemoryLimit ByteSize `yaml:"memory_limit"`
	GCPercent   int      `yaml:"gc_percent"`
}

// ---------------------------------------------------------------------------
// Scalar types
// ---------------------------------------------------------------------------

// Duration is a time.Duration written as "2s", "5m", "1h" or 0.
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// String formats the duration compactly ("2s", "5m0s" -> "5m").
func (d Duration) String() string {
	if d == 0 {
		return "0"
	}
	s := time.Duration(d).String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

// ParseDuration parses "2s", "1m30s", "0" or a bare number of seconds.
func ParseDuration(s string) (Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return Duration(n * float64(time.Second)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (want e.g. 2s, 5m, 1h, or 0)", s)
	}
	return Duration(d), nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("want a duration such as 2s, 5m or 1h")
	}
	v, err := ParseDuration(n.Value)
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// ByteSize is a byte count written as "1MiB", "64KiB", "512KB" or a number.
type ByteSize int64

var sizeUnits = []struct {
	suffix string
	mult   float64
}{
	{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30},
	{"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9},
	{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30},
	{"B", 1},
}

// ParseByteSize parses "1MiB", "64KiB", "12MiB", "1000000" or "0".
func ParseByteSize(s string) (ByteSize, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	mult := 1.0
	num := s
	for _, u := range sizeUnits {
		if strings.HasSuffix(s, u.suffix) {
			mult = u.mult
			num = strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			break
		}
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, fmt.Errorf("invalid size %q (want e.g. 64KiB, 1MiB, 12MiB)", s)
	}
	return ByteSize(f * mult), nil
}

// String formats the size with the largest exact binary unit.
func (b ByteSize) String() string {
	switch {
	case b == 0:
		return "0"
	case b%(1<<30) == 0:
		return strconv.FormatInt(int64(b)>>30, 10) + "GiB"
	case b%(1<<20) == 0:
		return strconv.FormatInt(int64(b)>>20, 10) + "MiB"
	case b%(1<<10) == 0:
		return strconv.FormatInt(int64(b)>>10, 10) + "KiB"
	default:
		return strconv.FormatInt(int64(b), 10)
	}
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (b *ByteSize) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("want a size such as 64KiB or 1MiB")
	}
	v, err := ParseByteSize(n.Value)
	if err != nil {
		return err
	}
	*b = v
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (b ByteSize) MarshalYAML() (any, error) { return b.String(), nil }

// ByteRate is a bytes-per-second rate written as "64KiB/s".
type ByteRate int64

// UnmarshalYAML implements yaml.Unmarshaler.
func (r *ByteRate) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("want a rate such as 64KiB/s")
	}
	v, err := ParseByteSize(strings.TrimSuffix(strings.TrimSpace(n.Value), "/s"))
	if err != nil {
		return fmt.Errorf("invalid rate %q (want e.g. 64KiB/s)", n.Value)
	}
	*r = ByteRate(v)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (r ByteRate) MarshalYAML() (any, error) { return ByteSize(r).String() + "/s", nil }

// Tri is a tri-state switch: auto, true or false.
type Tri string

// Tri values.
const (
	TriAuto  Tri = "auto"
	TriTrue  Tri = "true"
	TriFalse Tri = "false"
)

// UnmarshalYAML implements yaml.Unmarshaler.
func (t *Tri) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("want auto, true or false")
	}
	switch strings.ToLower(strings.TrimSpace(n.Value)) {
	case "auto", "":
		*t = TriAuto
	case "true", "yes", "on":
		*t = TriTrue
	case "false", "no", "off":
		*t = TriFalse
	default:
		return fmt.Errorf("invalid value %q (want auto, true or false)", n.Value)
	}
	return nil
}

// MarshalYAML implements yaml.Marshaler; booleans print unquoted.
func (t Tri) MarshalYAML() (any, error) {
	switch t {
	case TriTrue:
		return true, nil
	case TriFalse:
		return false, nil
	default:
		return "auto", nil
	}
}

// Off reports whether the switch is explicitly disabled.
func (t Tri) Off() bool { return t == TriFalse }

// On reports whether the switch is explicitly enabled.
func (t Tri) On() bool { return t == TriTrue }
