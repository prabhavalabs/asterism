// Package traefik derives IngressRoutes from Traefik dynamic configuration:
// Docker container labels (traefik.http.routers.* / services.*) and the
// Traefik API (GET /api/http/routers and /api/http/services).
package traefik

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/prabhavalabs/asterism/internal/collect/ingress"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

var (
	hostRe   = regexp.MustCompile("Host(?:SNI)?\\(([^)]*)\\)")
	prefixRe = regexp.MustCompile("PathPrefix\\(\\s*[`\"']([^`\"']+)[`\"']")
	quoteRe  = regexp.MustCompile("[`\"']([^`\"']+)[`\"']")
)

// ParseRule extracts hostnames and the first PathPrefix from a router rule
// such as "Host(`a.example.com`) && PathPrefix(`/api`)".
func ParseRule(rule string) (hosts []string, pathPrefix string) {
	for _, m := range hostRe.FindAllStringSubmatch(rule, -1) {
		for _, q := range quoteRe.FindAllStringSubmatch(m[1], -1) {
			hosts = ingress.AddHost(hosts, q[1])
		}
	}
	if m := prefixRe.FindStringSubmatch(rule); m != nil {
		pathPrefix = m[1]
	}
	if pathPrefix == "" {
		pathPrefix = "/"
	}
	return hosts, pathPrefix
}

// entrypointPort maps the conventional entrypoint names.
func entrypointPort(eps string, tls bool) (uint32, bool) {
	for _, ep := range strings.Split(eps, ",") {
		switch strings.TrimSpace(strings.ToLower(ep)) {
		case "websecure", "https", "web-secure":
			return 443, true
		case "web", "http":
			if !tls {
				return 80, false
			}
		}
	}
	if tls {
		return 443, true
	}
	return 80, false
}

type router struct {
	rule, entrypoints, service string
	tls                        bool
}

// ParseLabels returns the routes a container's traefik.* labels declare.
// upstream is "<container name>:<port>"; defaultPort is used when no
// loadbalancer.server.port label is set (0 = the container name only).
func ParseLabels(containerName string, labels map[string]string, defaultPort uint32) []*v1.IngressRoute {
	if strings.EqualFold(labels["traefik.enable"], "false") {
		return nil
	}
	routers := map[string]*router{}
	ports := map[string]string{}
	for k, v := range labels {
		rest, ok := strings.CutPrefix(k, "traefik.http.")
		if !ok {
			continue
		}
		parts := strings.SplitN(rest, ".", 3)
		if len(parts) < 3 {
			continue
		}
		switch parts[0] {
		case "routers":
			r := routers[parts[1]]
			if r == nil {
				r = &router{}
				routers[parts[1]] = r
			}
			switch {
			case parts[2] == "rule":
				r.rule = v
			case parts[2] == "entrypoints":
				r.entrypoints = v
			case parts[2] == "service":
				r.service = v
			case parts[2] == "tls":
				r.tls = strings.EqualFold(v, "true")
			case strings.HasPrefix(parts[2], "tls."):
				r.tls = true
			}
		case "services":
			if parts[2] == "loadbalancer.server.port" {
				ports[parts[1]] = v
			}
		}
	}
	names := make([]string, 0, len(routers))
	referenced := map[string]bool{}
	for n, r := range routers {
		names = append(names, n)
		referenced[strings.TrimSuffix(r.service, "@docker")] = true
	}
	sort.Strings(names)
	// A router without a service uses the container's only service, or the
	// only one no other router references.
	implicit := ""
	for svc := range ports {
		if !referenced[svc] {
			if implicit != "" {
				implicit = ""
				break
			}
			implicit = svc
		}
	}
	var routes []*v1.IngressRoute
	for _, n := range names {
		r := routers[n]
		if r.rule == "" {
			continue
		}
		hosts, p := ParseRule(r.rule)
		port, tls := entrypointPort(r.entrypoints, r.tls)
		svc := strings.TrimSuffix(r.service, "@docker")
		up := ""
		if svc == "" {
			svc = implicit
		}
		if pv, ok := ports[svc]; ok {
			up = pv
		} else if defaultPort > 0 {
			up = strconv.Itoa(int(defaultPort))
		}
		addr := containerName
		if up != "" {
			addr += ":" + up
		}
		key := n
		if len(hosts) > 0 {
			key = hosts[0]
		}
		routes = append(routes, &v1.IngressRoute{
			Id:         ingress.RouteID("traefik", key, p),
			ProxyKind:  v1.ProxyKind_PROXY_KIND_TRAEFIK,
			Hostnames:  hosts,
			ListenPort: port,
			Tls:        tls,
			PathPrefix: p,
			Strategy:   "single",
			Upstreams:  []*v1.UpstreamTarget{{Address: addr, Weight: 1, Healthy: true}},
		})
	}
	return ingress.Finish(routes)
}

// RoutesFromAPI converts the Traefik API's router and service lists.
func RoutesFromAPI(routersJSON, servicesJSON []byte, proxyWorkloadID string) ([]*v1.IngressRoute, error) {
	var rs []struct {
		Name        string    `json:"name"`
		Rule        string    `json:"rule"`
		Service     string    `json:"service"`
		Provider    string    `json:"provider"`
		EntryPoints []string  `json:"entryPoints"`
		TLS         *struct{} `json:"tls"`
		Status      string    `json:"status"`
	}
	var ss []struct {
		Name         string `json:"name"`
		Provider     string `json:"provider"`
		LoadBalancer *struct {
			Servers []struct {
				URL     string `json:"url"`
				Address string `json:"address"`
			} `json:"servers"`
		} `json:"loadBalancer"`
	}
	if err := json.Unmarshal(routersJSON, &rs); err != nil {
		return nil, fmt.Errorf("traefik routers: %w", err)
	}
	if err := json.Unmarshal(servicesJSON, &ss); err != nil {
		return nil, fmt.Errorf("traefik services: %w", err)
	}
	svcs := map[string][]*v1.UpstreamTarget{}
	for _, s := range ss {
		if s.LoadBalancer == nil {
			continue
		}
		var ups []*v1.UpstreamTarget
		for _, srv := range s.LoadBalancer.Servers {
			a := srv.URL
			if a == "" {
				a = srv.Address
			}
			ups = append(ups, &v1.UpstreamTarget{Address: ingress.UpstreamAddress(a), Weight: 1, Healthy: true})
		}
		svcs[s.Name] = ups
	}
	var routes []*v1.IngressRoute
	for _, r := range rs {
		if r.Rule == "" || (r.Status != "" && r.Status != "enabled") {
			continue
		}
		svc := r.Service
		if !strings.Contains(svc, "@") && r.Provider != "" {
			svc += "@" + r.Provider
		}
		ups := svcs[svc]
		if len(ups) == 0 {
			continue
		}
		hosts, p := ParseRule(r.Rule)
		port, tls := entrypointPort(strings.Join(r.EntryPoints, ","), r.TLS != nil)
		key := r.Name
		if len(hosts) > 0 {
			key = hosts[0]
		}
		routes = append(routes, &v1.IngressRoute{
			Id:              ingress.RouteID("traefik", key, p),
			ProxyKind:       v1.ProxyKind_PROXY_KIND_TRAEFIK,
			ProxyWorkloadId: proxyWorkloadID,
			Hostnames:       hosts,
			ListenPort:      port,
			Tls:             tls,
			PathPrefix:      p,
			Strategy:        ingress.Strategy("", len(ups)),
			Upstreams:       ups,
		})
	}
	return ingress.Finish(routes), nil
}
