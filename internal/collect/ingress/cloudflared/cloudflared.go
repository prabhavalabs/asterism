// Package cloudflared parses a cloudflared tunnel config.yml "ingress:" list
// into IngressRoutes. The credentials-file and tunnel tokens are never
// opened or decoded; only hostname, path and service are read.
package cloudflared

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/prabhavalabs/asterism/internal/collect/ingress"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

type config struct {
	Tunnel  string `yaml:"tunnel"`
	Ingress []struct {
		Hostname string `yaml:"hostname"`
		Path     string `yaml:"path"`
		Service  string `yaml:"service"`
	} `yaml:"ingress"`
}

// Parse returns one route per ingress rule with a proxied service.
// Catch-all rules (no hostname) and built-in services (http_status:404,
// hello_world, bastion) are skipped.
func Parse(src []byte, proxyWorkloadID string) ([]*v1.IngressRoute, error) {
	if len(src) > ingress.MaxFileBytes {
		return nil, ingress.ErrTooLarge
	}
	var c config
	if err := yaml.Unmarshal(src, &c); err != nil {
		return nil, fmt.Errorf("cloudflared config: %w", err)
	}
	var routes []*v1.IngressRoute
	for _, r := range c.Ingress {
		svc := strings.TrimSpace(r.Service)
		if r.Hostname == "" || svc == "" || strings.HasPrefix(svc, "http_status:") || svc == "hello_world" || svc == "bastion" || svc == "socks5" {
			continue
		}
		addr := ingress.UpstreamAddress(strings.TrimPrefix(strings.TrimPrefix(svc, "ssh://"), "tcp://"))
		if strings.HasPrefix(svc, "unix:") || strings.HasPrefix(svc, "unix+tls:") {
			addr = "unix:" + strings.TrimPrefix(strings.TrimPrefix(svc, "unix+tls:"), "unix:")
		}
		host := strings.ToLower(r.Hostname)
		p := pathPrefix(r.Path)
		routes = append(routes, &v1.IngressRoute{
			Id:              ingress.RouteID("cloudflared", host, p),
			ProxyKind:       v1.ProxyKind_PROXY_KIND_CLOUDFLARED,
			ProxyWorkloadId: proxyWorkloadID,
			Hostnames:       []string{host},
			ListenPort:      443,
			Tls:             true,
			PathPrefix:      p,
			Strategy:        "tunnel",
			Upstreams:       []*v1.UpstreamTarget{{Address: addr, Weight: 1, Healthy: true}},
		})
	}
	return ingress.Finish(routes), nil
}

// pathPrefix turns cloudflared's path regex into a literal prefix when it
// has one ("^/api/.*" -> "/api/"), otherwise "/".
func pathPrefix(re string) string {
	re = strings.TrimPrefix(re, "^")
	end := strings.IndexAny(re, ".*+?()[]{}|$\\")
	if end >= 0 {
		re = re[:end]
	}
	if re == "" || re[0] != '/' {
		return "/"
	}
	return re
}
