// Package proxy reads reverse-proxy configuration (nginx, Caddyfile and the
// Caddy admin API, HAProxy, cloudflared, and the Traefik API) into
// IngressRoutes, plus the manual sources.routes. Config files are re-parsed
// only when the size or mtime of a file read last time (or of an include
// directory) changes, or when the Caddy/Traefik API response hash changes.
// TLS keys, cloudflared credentials and tunnel tokens are never opened.
package proxy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/procfs"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	"github.com/prabhavalabs/asterism/internal/collect/ingress"
	"github.com/prabhavalabs/asterism/internal/collect/ingress/caddy"
	"github.com/prabhavalabs/asterism/internal/collect/ingress/cloudflared"
	"github.com/prabhavalabs/asterism/internal/collect/ingress/haproxy"
	"github.com/prabhavalabs/asterism/internal/collect/ingress/nginx"
	"github.com/prabhavalabs/asterism/internal/collect/ingress/traefik"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// maxAPIBytes caps admin API responses.
const maxAPIBytes = 8 << 20

// Source is the proxy collector.
type Source struct {
	cfg      config.Proxies
	routes   []config.Route
	interval time.Duration

	log    *slog.Logger
	fs     HostFS
	cgRoot string
	files  []*fileProxy
	http   *http.Client
	// After replaces time.After in Run (tests).
	After func(time.Duration) <-chan time.Time

	noCaddyAPI, noTraefikAPI bool

	mu       sync.Mutex
	apiCaddy []*v1.IngressRoute
	apiTrae  []*v1.IngressRoute
	apiHash  [2][32]byte
}

// fileProxy is one file-configured proxy.
type fileProxy struct {
	kind    string // "nginx", "caddy", "haproxy", "cloudflared"
	main    string // host path
	unit    string // systemd unit to attribute routes to
	routes  []*v1.IngressRoute
	stamp   string // fingerprint of the files read last time
	files   []string
	dirs    []string
	lastErr string
}

// New returns a proxy source.
func New(cfg config.Proxies, routes []config.Route, interval time.Duration) *Source {
	return &Source{cfg: cfg, routes: routes, interval: interval, After: time.After,
		http: &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Name implements state.Source.
func (s *Source) Name() string { return "proxies" }

// Interval implements state.Source.
func (s *Source) Interval() time.Duration { return s.interval }

// Init implements state.Source.
func (s *Source) Init(ctx context.Context, env state.Env) ([]string, error) {
	s.log = env.Logger
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.fs = HostFS{Root: env.Root}
	s.cgRoot = env.HostPath(procfs.CgroupRoot)
	add := func(kind string, enabled config.Tri, p string) error {
		if enabled.Off() || p == "" {
			return nil
		}
		if _, err := s.fs.Stat(p); err != nil {
			if enabled.On() {
				return fmt.Errorf("%s config %s: %w", kind, p, err)
			}
			return nil
		}
		s.files = append(s.files, &fileProxy{kind: kind, main: p, unit: kind + ".service"})
		return nil
	}
	var errs []error
	errs = append(errs, add("nginx", s.cfg.Nginx.Enabled, s.cfg.Nginx.Config))
	errs = append(errs, add("caddy", s.cfg.Caddy.Enabled, s.cfg.Caddy.Caddyfile))
	errs = append(errs, add("haproxy", s.cfg.HAProxy.Enabled, s.cfg.HAProxy.Config))
	errs = append(errs, add("cloudflared", s.cfg.Cloudflared.Enabled, s.cfg.Cloudflared.Config))
	if err := errors.Join(errs...); err != nil {
		s.log.Warn("proxy config missing", "err", err)
	}
	var caps []string
	for _, f := range s.files {
		caps = append(caps, f.kind)
	}
	// With enabled: auto an admin API counts only if it answers now.
	if base := s.caddyAPI(); base != "" {
		if s.cfg.Caddy.Enabled.On() || s.probe(ctx, base+"/config/") {
			caps = append(caps, "caddy_api")
		} else {
			s.noCaddyAPI = true
		}
	}
	if base := s.traefikAPI(); base != "" {
		if s.cfg.Traefik.Enabled.On() || s.probe(ctx, base+"/api/http/routers") {
			caps = append(caps, "traefik_api")
		} else {
			s.noTraefikAPI = true
		}
	}
	if len(caps) == 0 && len(s.routes) == 0 {
		return nil, errors.New("no proxy configuration found and no manual routes")
	}
	return caps, nil
}

func (s *Source) probe(ctx context.Context, url string) bool {
	pctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, err := s.get(pctx, url)
	return err == nil
}

func (s *Source) caddyAPI() string {
	if s.cfg.Caddy.Enabled.Off() || s.noCaddyAPI {
		return ""
	}
	return strings.TrimSuffix(s.cfg.Caddy.AdminAPI, "/")
}

func (s *Source) traefikAPI() string {
	if s.cfg.Traefik.Enabled.Off() || s.noTraefikAPI {
		return ""
	}
	return strings.TrimSuffix(s.cfg.Traefik.API, "/")
}

// Tick implements state.Source.
func (s *Source) Tick(_ time.Time, sink state.Sink) error {
	var all []*v1.IngressRoute
	for _, f := range s.files {
		s.refresh(f)
		all = append(all, f.routes...)
	}
	s.mu.Lock()
	all = append(all, s.apiCaddy...)
	all = append(all, s.apiTrae...)
	s.mu.Unlock()
	all = append(all, manualRoutes(s.routes)...)
	sink.ReplaceRoutes(ingress.Finish(all))
	return nil
}

// fingerprint stats the files and directories read last time.
func (s *Source) fingerprint(f *fileProxy) string {
	var b strings.Builder
	for _, p := range append(append([]string{f.main}, f.files...), f.dirs...) {
		if st, err := s.fs.Stat(p); err == nil {
			fmt.Fprintf(&b, "%s|%d|%d;", p, st.Size(), st.ModTime().UnixNano())
		} else {
			fmt.Fprintf(&b, "%s|-;", p)
		}
	}
	return b.String()
}

func (s *Source) refresh(f *fileProxy) {
	stamp := s.fingerprint(f)
	if stamp == f.stamp && f.stamp != "" {
		return
	}
	proxyID := ""
	if st, err := os.Stat(filepath.Join(s.cgRoot, "system.slice", f.unit)); err == nil && st.IsDir() {
		proxyID = "unit:" + f.unit
	}
	var routes []*v1.IngressRoute
	var files, dirs []string
	var err error
	switch f.kind {
	case "nginx":
		var cfg *nginx.Config
		routes, cfg, err = nginx.Routes(s.fs, f.main, proxyID)
		if cfg != nil {
			files = cfg.Files
			for _, g := range cfg.Globs {
				dirs = append(dirs, path.Dir(g))
			}
			if len(cfg.Errors) > 0 {
				s.log.Debug("nginx config problems", "errors", cfg.Errors)
			}
		}
	case "caddy":
		var cf *caddy.Caddyfile
		cf, err = caddy.LoadCaddyfile(s.fs, f.main)
		if cf != nil {
			routes, files = cf.Routes(proxyID), cf.Files
		}
	case "haproxy":
		var b []byte
		if b, err = s.fs.ReadFile(f.main); err == nil {
			routes, files = haproxy.Parse(b, proxyID), []string{f.main}
		}
	case "cloudflared":
		var b []byte
		if b, err = s.fs.ReadFile(f.main); err == nil {
			routes, err = cloudflared.Parse(b, proxyID)
			files = []string{f.main}
		}
	}
	if err != nil {
		if msg := err.Error(); msg != f.lastErr {
			s.log.Warn("proxy config unreadable", "proxy", f.kind, "path", f.main, "err", err)
			f.lastErr = msg
		}
		f.routes = nil
	} else {
		f.lastErr = ""
		f.routes = routes
	}
	f.files, f.dirs = files, dirs
	f.stamp = s.fingerprint(f)
}

// Run implements state.Runner: it polls the Caddy and Traefik APIs (when
// configured) so Tick never waits on the network.
func (s *Source) Run(ctx context.Context, _ state.Sink) error {
	if s.caddyAPI() == "" && s.traefikAPI() == "" {
		return nil
	}
	for {
		s.PollAPIs(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-s.After(s.interval):
		}
	}
}

// PollAPIs fetches the admin APIs once.
func (s *Source) PollAPIs(ctx context.Context) {
	if base := s.caddyAPI(); base != "" {
		if b, err := s.get(ctx, base+"/config/"); err == nil {
			h := sha256.Sum256(b)
			s.mu.Lock()
			changed := h != s.apiHash[0]
			s.mu.Unlock()
			if changed {
				if routes, err := caddy.RoutesFromJSON(b, "unit:caddy.service"); err == nil {
					s.mu.Lock()
					s.apiCaddy, s.apiHash[0] = routes, h
					s.mu.Unlock()
				}
			}
		}
	}
	if base := s.traefikAPI(); base != "" {
		rb, err1 := s.get(ctx, base+"/api/http/routers")
		sb, err2 := s.get(ctx, base+"/api/http/services")
		if err1 == nil && err2 == nil {
			h := sha256.Sum256(append(append([]byte{}, rb...), sb...))
			s.mu.Lock()
			changed := h != s.apiHash[1]
			s.mu.Unlock()
			if changed {
				if routes, err := traefik.RoutesFromAPI(rb, sb, ""); err == nil {
					s.mu.Lock()
					s.apiTrae, s.apiHash[1] = routes, h
					s.mu.Unlock()
				}
			}
		}
	}
}

func (s *Source) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxAPIBytes {
		return nil, ingress.ErrTooLarge
	}
	return b, nil
}

func proxyKind(name string) v1.ProxyKind {
	switch strings.ToLower(name) {
	case "nginx":
		return v1.ProxyKind_PROXY_KIND_NGINX
	case "caddy":
		return v1.ProxyKind_PROXY_KIND_CADDY
	case "traefik":
		return v1.ProxyKind_PROXY_KIND_TRAEFIK
	case "cloudflared", "cloudflare":
		return v1.ProxyKind_PROXY_KIND_CLOUDFLARED
	case "haproxy":
		return v1.ProxyKind_PROXY_KIND_HAPROXY
	}
	return v1.ProxyKind_PROXY_KIND_UNSPECIFIED
}

// manualRoutes converts sources.routes (e.g. remote-managed tunnels).
func manualRoutes(rs []config.Route) []*v1.IngressRoute {
	out := make([]*v1.IngressRoute, 0, len(rs))
	for _, r := range rs {
		if len(r.Hostnames) == 0 || r.Upstream == "" {
			continue
		}
		kind := strings.ToLower(r.Proxy)
		if kind == "" {
			kind = "manual"
		}
		port := uint32(r.ListenPort)
		tls := r.TLS || kind == "cloudflared"
		if port == 0 {
			port = 80
			if tls {
				port = 443
			}
		}
		p := r.PathPrefix
		if p == "" {
			p = "/"
		}
		var hosts []string
		for _, h := range r.Hostnames {
			hosts = ingress.AddHost(hosts, h)
		}
		if len(hosts) == 0 {
			continue
		}
		out = append(out, &v1.IngressRoute{
			Id:         ingress.RouteID(kind, hosts[0], p),
			ProxyKind:  proxyKind(kind),
			Hostnames:  hosts,
			ListenPort: port,
			Tls:        tls,
			PathPrefix: p,
			Strategy:   "single",
			Upstreams:  []*v1.UpstreamTarget{{Address: ingress.UpstreamAddress(r.Upstream), Weight: 1, Healthy: true}},
		})
	}
	return out
}

// Proxies lists the detected file-configured proxies and their main files.
func (s *Source) Proxies() map[string]string {
	out := map[string]string{}
	for _, f := range s.files {
		out[f.kind] = f.main
	}
	return out
}
