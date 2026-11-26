package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Validate checks semantic constraints and returns errors and warnings,
// including any warnings recorded while loading. It reads the token file
// (when one is configured) to confirm a token is available.
func (c *Config) Validate() Issues {
	v := validator{cfg: c}
	v.issues = append(v.issues, c.warnings...)
	v.run()
	return v.issues.sorted()
}

type validator struct {
	cfg    *Config
	issues Issues
}

func (v *validator) add(sev Severity, p, format string, args ...any) {
	v.issues = append(v.issues, Issue{
		Severity: sev, File: v.cfg.file, Line: v.cfg.lines[p], Path: p,
		Message: fmt.Sprintf(format, args...),
	})
}

func (v *validator) errf(p, format string, args ...any)  { v.add(SeverityError, p, format, args...) }
func (v *validator) warnf(p, format string, args ...any) { v.add(SeverityWarning, p, format, args...) }

func (v *validator) oneOf(p, val string, allowed ...string) {
	if !slices.Contains(allowed, val) {
		v.errf(p, "invalid value %q (want %s)", val, orList(allowed))
	}
}

func orList(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	case 2:
		return s[0] + " or " + s[1]
	default:
		return strings.Join(s[:len(s)-1], ", ") + " or " + s[len(s)-1]
	}
}

var (
	slugRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	tokenNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

func (v *validator) run() {
	c := v.cfg
	if c.Version != 0 && c.Version != 1 {
		v.errf("version", "unsupported version %d (this agent understands version 1)", c.Version)
	}
	v.hub()
	v.host()
	v.intervals()
	v.sources()
	v.projects()
	for i, re := range c.Redaction.ExtraSecretKeys {
		if _, err := regexp.Compile(re); err != nil {
			v.errf(fmt.Sprintf("redaction.extra_secret_keys[%d]", i), "invalid regular expression %q: %v", re, err)
		}
	}
	v.globs("redaction.drop_labels", c.Redaction.DropLabels)
	v.actions()
	v.agent()
}

func (v *validator) hub() {
	h := v.cfg.Hub
	switch {
	case strings.TrimSpace(h.URL) == "":
		v.errf("hub.url", "required: set hub.url (e.g. https://asterism.example.com) or %s", EnvHubURL)
	default:
		u, err := url.Parse(h.URL)
		switch {
		case err != nil || u.Host == "":
			v.errf("hub.url", "invalid URL %q (want e.g. https://asterism.example.com)", h.URL)
		case u.Scheme == "https":
		case u.Scheme == "http":
			if !IsLoopbackHost(u.Hostname()) && !h.TLS.InsecureDev {
				v.errf("hub.url", "http:// is allowed only for 127.0.0.1, ::1 or localhost; use https:// or set hub.tls.insecure_dev: true for a development hub")
			}
		default:
			v.errf("hub.url", "unsupported scheme %q (want https, or http for a loopback hub)", u.Scheme)
		}
		if err == nil && u.User != nil {
			v.errf("hub.url", "must not contain credentials; put the token in hub.token_file")
		}
	}
	if _, err := v.cfg.ResolveToken(); err != nil {
		p := "hub.token_file"
		if v.cfg.Hub.Token != "" || v.cfg.Hub.TokenFile == "" {
			p = "hub.token"
		}
		v.errf(p, "%v", err)
	} else if tok, _ := v.cfg.ResolveToken(); !strings.HasPrefix(tok, "asta_") {
		v.warnf("hub.token", "token does not start with asta_; create one on the hub with `asterism agent token create --host <name>`")
	}
	if h.TokenFile != "" && h.Token == "" {
		if st, err := os.Stat(h.TokenFile); err == nil && st.Mode().Perm()&0o004 != 0 {
			v.warnf("hub.token_file", "%s is world-readable; run chmod 0640 %s", h.TokenFile, h.TokenFile)
		}
	}
	v.oneOf("hub.transport", h.Transport, "auto", "bidi", "unary")
	v.oneOf("hub.compression", h.Compression, "gzip", "none")
	t := h.TLS
	if t.CAFile != "" {
		if _, err := os.ReadFile(t.CAFile); err != nil {
			v.errf("hub.tls.ca_file", "cannot read %s: %v", t.CAFile, unwrapPathErr(err))
		}
	}
	if (t.ClientCert == "") != (t.ClientKey == "") {
		v.errf("hub.tls", "client_cert and client_key must be set together for mTLS")
	}
	for i, pin := range t.PinSHA256 {
		if _, err := DecodePin(pin); err != nil {
			v.errf(fmt.Sprintf("hub.tls.pin_sha256[%d]", i), "%v", err)
		}
	}
	if t.InsecureDev {
		v.warnf("hub.tls.insecure_dev", "plain http:// to a non-loopback hub sends the token and all data unencrypted; use only for development")
	}
	if h.Buffer.MaxBytes < 64<<10 || h.Buffer.MaxBytes > 64<<20 {
		v.errf("hub.buffer.max_bytes", "%s is out of range (want 64KiB to 64MiB)", h.Buffer.MaxBytes)
	}
	if h.Buffer.MaxEvents < 1 || h.Buffer.MaxEvents > 100000 {
		v.errf("hub.buffer.max_events", "%d is out of range (want 1 to 100000)", h.Buffer.MaxEvents)
	}
	if h.Backoff.Initial.D() < 100*time.Millisecond {
		v.errf("hub.backoff.initial", "%s is too short (want at least 100ms)", h.Backoff.Initial)
	}
	if h.Backoff.Max < h.Backoff.Initial {
		v.errf("hub.backoff.max", "%s is shorter than hub.backoff.initial (%s)", h.Backoff.Max, h.Backoff.Initial)
	}
}

func (v *validator) host() {
	h := v.cfg.Host
	if h.Name != "" && !slugRe.MatchString(h.Name) {
		v.errf("host.name", "invalid host name %q (use lowercase letters, digits, '.', '_' and '-')", h.Name)
	}
	if !path.IsAbs(h.Root) {
		v.errf("host.root", "must be an absolute path such as / or /host, got %q", h.Root)
	}
	for i, fs := range h.Filesystems {
		if !path.IsAbs(fs) {
			v.errf(fmt.Sprintf("host.filesystems[%d]", i), "must be an absolute mount point, got %q", fs)
		}
	}
	v.globs("host.interfaces.exclude", h.Interfaces.Exclude)
}

func (v *validator) intervals() {
	iv := v.cfg.Intervals
	pos := []struct {
		key string
		d   Duration
	}{
		{"host", iv.Host}, {"containers", iv.Containers}, {"listeners", iv.Listeners},
		{"units", iv.Units}, {"processes", iv.Processes}, {"proxies", iv.Proxies},
		{"docker_resources", iv.DockerResources}, {"inventory_resync", iv.InventoryResync},
		{"keyframe", iv.Keyframe}, {"flush", iv.Flush}, {"min", iv.Min}, {"max", iv.Max},
	}
	for _, p := range pos {
		if p.d.D() < 500*time.Millisecond {
			v.errf("intervals."+p.key, "%s is too short (want at least 500ms)", p.d)
		}
	}
	if iv.DockerDiskUsage != 0 && iv.DockerDiskUsage.D() < time.Minute {
		v.errf("intervals.docker_disk_usage", "%s is too short (want 0 to disable, or at least 1m: system df walks every volume)", iv.DockerDiskUsage)
	}
	if iv.Min > iv.Max {
		v.errf("intervals.min", "%s is longer than intervals.max (%s)", iv.Min, iv.Max)
	}
	if iv.IdleFactor < 1 || iv.IdleFactor > 20 {
		v.errf("intervals.idle_factor", "%g is out of range (want 1 to 20)", iv.IdleFactor)
	}
	if iv.Keyframe < iv.Flush {
		v.errf("intervals.keyframe", "%s is shorter than intervals.flush (%s)", iv.Keyframe, iv.Flush)
	}
	if iv.Flush.D() > 60*time.Second {
		v.errf("intervals.flush", "%s is too long (want at most 60s; the hub marks the agent stale after 30s without data)", iv.Flush)
	}
}

func (v *validator) sources() {
	s := v.cfg.Sources
	v.endpoint("sources.docker.endpoint", s.Docker.Endpoint)
	v.globs("sources.docker.include.names", s.Docker.Include.Names)
	v.globs("sources.docker.exclude.names", s.Docker.Exclude.Names)
	switch g := s.Docker.GroupBy; {
	case g == "compose" || g == "none":
	case strings.HasPrefix(g, "label:") && len(g) > len("label:"):
	default:
		v.errf("sources.docker.group_by", "invalid value %q (want compose, none or label:<key>)", g)
	}
	v.oneOf("sources.docker.stats", s.Docker.Stats, "cgroup", "api")
	if !s.Podman.Enabled.Off() {
		v.endpoint("sources.podman.endpoint", s.Podman.Endpoint)
	}
	v.oneOf("sources.systemd.discover", s.Systemd.Discover, "listening", "include-only", "all")
	v.globs("sources.systemd.include", s.Systemd.Include)
	v.globs("sources.systemd.exclude", s.Systemd.Exclude)
	if s.Systemd.Discover == "include-only" && len(s.Systemd.Include) == 0 && !s.Systemd.Enabled.Off() {
		v.warnf("sources.systemd.include", "discover is include-only but include is empty, so no units are collected")
	}
	names := map[string]bool{}
	for i, m := range s.Processes.Match {
		p := fmt.Sprintf("sources.processes.match[%d]", i)
		switch {
		case m.Name == "":
			v.errf(p+".name", "required: the workload becomes proc:<name>")
		case !slugRe.MatchString(m.Name):
			v.errf(p+".name", "invalid name %q (use lowercase letters, digits, '.', '_' and '-')", m.Name)
		case names[m.Name]:
			v.errf(p+".name", "duplicate process name %q", m.Name)
		}
		names[m.Name] = true
		if m.Exe == "" && m.Cmdline == "" {
			v.errf(p, "set exe or cmdline so the rule matches specific processes")
		}
		v.globs(p+".cmdline", []string{m.Cmdline})
		if m.Role != "" {
			v.oneOf(p+".role", m.Role, "proxy", "app", "worker", "database", "cache", "queue", "storage")
		}
	}
	for i, h := range s.PM2.Homes {
		if !path.IsAbs(h) {
			v.errf(fmt.Sprintf("sources.pm2.homes[%d]", i), "must be an absolute path such as /home/deploy/.pm2, got %q", h)
		}
	}
	px := s.Proxies
	v.absIfSet("sources.proxies.nginx.config", px.Nginx.Config)
	v.absIfSet("sources.proxies.caddy.caddyfile", px.Caddy.Caddyfile)
	v.absIfSet("sources.proxies.haproxy.config", px.HAProxy.Config)
	v.absIfSet("sources.proxies.cloudflared.config", px.Cloudflared.Config)
	v.httpURL("sources.proxies.caddy.admin_api", px.Caddy.AdminAPI)
	v.httpURL("sources.proxies.traefik.api", px.Traefik.API)
	for i, proto := range s.Ports.Protocols {
		v.oneOf(fmt.Sprintf("sources.ports.protocols[%d]", i), proto, "tcp", "udp")
	}
	for i, r := range s.Routes {
		p := fmt.Sprintf("sources.routes[%d]", i)
		v.oneOf(p+".proxy", r.Proxy, "nginx", "caddy", "traefik", "cloudflared", "haproxy")
		if len(r.Hostnames) == 0 {
			v.errf(p+".hostnames", "required: list at least one hostname")
		}
		if r.Upstream == "" {
			v.errf(p+".upstream", "required, e.g. http://127.0.0.1:3001")
		} else if u, err := url.Parse(r.Upstream); err != nil || u.Host == "" {
			v.errf(p+".upstream", "invalid upstream %q (want e.g. http://127.0.0.1:3001)", r.Upstream)
		}
		if r.ListenPort < 0 || r.ListenPort > 65535 {
			v.errf(p+".listen_port", "%d is out of range (want 0 to 65535)", r.ListenPort)
		}
	}
	l := s.Logs
	if l.MaxLines < 1 || l.MaxLines > 500 {
		v.errf("sources.logs.max_lines", "%d is out of range (want 1 to 500)", l.MaxLines)
	}
	if l.MaxFollow.D() < 0 || l.MaxFollow.D() > 10*time.Minute {
		v.errf("sources.logs.max_follow", "%s is out of range (want 0 to 10m)", l.MaxFollow)
	}
	if l.RateLimit < 1<<10 || l.RateLimit > 1<<20 {
		v.errf("sources.logs.rate_limit", "%s/s is out of range (want 1KiB/s to 1MiB/s)", ByteSize(l.RateLimit))
	}
	for i, f := range l.Files {
		p := fmt.Sprintf("sources.logs.files[%d]", i)
		if !strings.HasPrefix(f.Workload, "proc:") && !strings.HasPrefix(f.Workload, "unit:") && !strings.HasPrefix(f.Workload, "ctr:") {
			v.errf(p+".workload", "invalid workload id %q (want proc:<name>, unit:<name> or ctr:<name>)", f.Workload)
		}
		if !path.IsAbs(f.Path) {
			v.errf(p+".path", "must be an absolute file path, got %q", f.Path)
		}
	}
}

func (v *validator) projects() {
	seen := map[string]bool{}
	for i, p := range v.cfg.Projects {
		base := fmt.Sprintf("projects[%d]", i)
		switch {
		case p.Name == "":
			v.errf(base+".name", "required")
		case !slugRe.MatchString(p.Name):
			v.errf(base+".name", "invalid name %q (use lowercase letters, digits, '.', '_' and '-')", p.Name)
		case seen[p.Name]:
			v.errf(base+".name", "duplicate project %q", p.Name)
		}
		seen[p.Name] = true
		if p.Icon != "" && !slugRe.MatchString(p.Icon) {
			v.errf(base+".icon", "invalid icon %q (want a Simple Icons slug such as nodedotjs or postgresql)", p.Icon)
		}
		if p.Color != "" && !tokenNameRe.MatchString(p.Color) {
			v.errf(base+".color", "invalid colour %q (want a design-token name such as teal or violet, not a hex value)", p.Color)
		}
		m := p.Members
		if len(m.Compose)+len(m.Containers)+len(m.Units)+len(m.PM2)+len(m.Processes) == 0 {
			v.errf(base+".members", "list at least one of compose, containers, units, pm2 or processes")
		}
		v.globs(base+".members.compose", m.Compose)
		v.globs(base+".members.containers", m.Containers)
		v.globs(base+".members.units", m.Units)
		v.globs(base+".members.pm2", m.PM2)
		v.globs(base+".members.processes", m.Processes)
	}
}

// ActionNames are the container actions the agent understands.
var ActionNames = []string{"start", "stop", "restart", "pause", "unpause"}

func (v *validator) actions() {
	a := v.cfg.Actions
	for i, name := range a.Allow {
		v.oneOf(fmt.Sprintf("actions.allow[%d]", i), name, ActionNames...)
	}
	v.globs("actions.containers", a.Containers)
	v.globs("actions.protect", a.Protect)
	if a.Enabled {
		switch {
		case len(a.Allow) == 0:
			v.warnf("actions.allow", "actions.enabled is true but allow is empty, so every action is refused")
		case len(a.Containers) == 0:
			v.warnf("actions.containers", "actions.enabled is true but containers is empty, so every container is refused; use [\"*\"] to allow all")
		}
	}
}

func (v *validator) agent() {
	a := v.cfg.Agent
	v.oneOf("agent.log_level", a.LogLevel, "debug", "info", "warn", "error")
	v.oneOf("agent.log_format", a.LogFormat, "text", "json")
	if a.GOMAXPROCS < 0 || a.GOMAXPROCS > 256 {
		v.errf("agent.gomaxprocs", "%d is out of range (want 0 for the Go default, or 1 to 256)", a.GOMAXPROCS)
	}
	if a.MemoryLimit != 0 && a.MemoryLimit < 4<<20 {
		v.errf("agent.memory_limit", "%s is too small (want 0 for none, or at least 4MiB)", a.MemoryLimit)
	}
	if a.GCPercent < -1 || a.GCPercent == 0 || a.GCPercent > 1000 {
		v.errf("agent.gc_percent", "%d is out of range (want -1 to disable, or 1 to 1000)", a.GCPercent)
	}
}

func (v *validator) globs(p string, globs []string) {
	for i, g := range globs {
		if _, err := path.Match(g, ""); err != nil {
			v.errf(fmt.Sprintf("%s[%d]", p, i), "invalid glob %q: %v", g, err)
		}
	}
}

func (v *validator) absIfSet(p, val string) {
	if val != "" && !path.IsAbs(val) {
		v.errf(p, "must be an absolute path, got %q", val)
	}
}

func (v *validator) httpURL(p, val string) {
	if val == "" {
		return
	}
	u, err := url.Parse(val)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		v.errf(p, "invalid URL %q (want e.g. http://127.0.0.1:2019)", val)
	}
}

func (v *validator) endpoint(p, val string) {
	switch {
	case strings.HasPrefix(val, "unix://"):
		if !path.IsAbs(strings.TrimPrefix(val, "unix://")) {
			v.errf(p, "invalid unix socket %q (want unix:///var/run/docker.sock)", val)
		}
	case strings.HasPrefix(val, "tcp://"), strings.HasPrefix(val, "http://"):
		u, err := url.Parse(val)
		if err != nil || u.Port() == "" {
			v.errf(p, "invalid endpoint %q (want tcp://host:port, e.g. tcp://socket-proxy:2375)", val)
		}
	default:
		v.errf(p, "invalid endpoint %q (want unix:///path/to.sock or tcp://host:port)", val)
	}
}

// IsLoopbackHost reports whether host is localhost or a loopback IP.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// DecodePin decodes a base64 SHA-256 SPKI pin ("sha256/" prefix optional).
func DecodePin(pin string) ([]byte, error) {
	s := strings.TrimPrefix(strings.TrimSpace(pin), "sha256/")
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
	}
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("invalid pin %q (want the base64 SHA-256 of the hub certificate's public key; get it with: openssl x509 -in hub.crt -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64)", pin)
	}
	return b, nil
}

// ResolveToken returns the hub token from hub.token or hub.token_file.
func (c *Config) ResolveToken() (string, error) {
	if t := strings.TrimSpace(c.Hub.Token); t != "" {
		return t, nil
	}
	if c.Hub.TokenFile == "" {
		return "", fmt.Errorf("required: set hub.token_file (or hub.token: ${%s})", EnvToken)
	}
	b, err := os.ReadFile(c.Hub.TokenFile)
	if err != nil {
		return "", fmt.Errorf("cannot read token file %s: %v (create it with `asterism agent token create` on the hub, or set %s)", c.Hub.TokenFile, unwrapPathErr(err), EnvToken)
	}
	if len(b) > 4096 {
		return "", fmt.Errorf("token file %s is too large to be a token", c.Hub.TokenFile)
	}
	t := strings.TrimSpace(string(b))
	if t == "" {
		return "", fmt.Errorf("token file %s is empty", c.Hub.TokenFile)
	}
	return t, nil
}

func unwrapPathErr(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
