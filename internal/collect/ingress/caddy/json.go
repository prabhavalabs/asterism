package caddy

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/prabhavalabs/asterism/internal/collect/ingress"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

type jsonConfig struct {
	Apps struct {
		HTTP struct {
			Servers map[string]jsonServer `json:"servers"`
		} `json:"http"`
	} `json:"apps"`
}

type jsonServer struct {
	Listen      []string        `json:"listen"`
	Routes      []jsonRoute     `json:"routes"`
	TLSPolicies json.RawMessage `json:"tls_connection_policies"`
}

type jsonRoute struct {
	Match  []jsonMatch       `json:"match"`
	Handle []json.RawMessage `json:"handle"`
}

type jsonMatch struct {
	Host []string `json:"host"`
	Path []string `json:"path"`
}

type jsonHandler struct {
	Handler   string `json:"handler"`
	Upstreams []struct {
		Dial string `json:"dial"`
	} `json:"upstreams"`
	LoadBalancing *struct {
		SelectionPolicy struct {
			Policy string `json:"policy"`
		} `json:"selection_policy"`
	} `json:"load_balancing"`
	Routes []jsonRoute `json:"routes"` // subroute
	// Strip prefix (handle_path) is recorded by rewrite handlers; the
	// route's path matcher already carries the prefix.
}

// RoutesFromJSON parses the admin API's GET /config/ response. TLS
// automation secrets (ACME account keys, DNS provider tokens) live under
// apps.tls and are never decoded.
func RoutesFromJSON(raw []byte, proxyWorkloadID string) ([]*v1.IngressRoute, error) {
	if len(raw) > ingress.MaxFileBytes*8 {
		return nil, ingress.ErrTooLarge
	}
	var cfg jsonConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("decode caddy config: %w", err)
	}
	var routes []*v1.IngressRoute
	names := make([]string, 0, len(cfg.Apps.HTTP.Servers))
	for n := range cfg.Apps.HTTP.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		srv := cfg.Apps.HTTP.Servers[name]
		port := uint32(0)
		for _, l := range srv.Listen {
			if i := strings.LastIndexByte(l, ':'); i >= 0 {
				if p, err := strconv.Atoi(l[i+1:]); err == nil && p > 0 && p < 65536 {
					if port == 0 || p == 443 {
						port = uint32(p)
					}
				}
			}
		}
		tls := port == 443 || len(srv.TLSPolicies) > 2
		var out []proxied
		hostsBy := map[int][]string{}
		walkJSON(srv.Routes, nil, "/", &out, hostsBy, 0)
		for i, p := range out {
			hosts := hostsBy[i]
			key := ":" + strconv.Itoa(int(port))
			if len(hosts) > 0 {
				key = hosts[0]
			}
			routes = append(routes, &v1.IngressRoute{
				Id:              ingress.RouteID("caddy", key, p.path),
				ProxyKind:       v1.ProxyKind_PROXY_KIND_CADDY,
				ProxyWorkloadId: proxyWorkloadID,
				Hostnames:       hosts,
				ListenPort:      port,
				Tls:             tls && len(hosts) > 0,
				PathPrefix:      p.path,
				Strategy:        ingress.Strategy(p.policy, len(p.ups)),
				Upstreams:       p.ups,
			})
		}
	}
	return ingress.Finish(routes), nil
}

func walkJSON(rs []jsonRoute, hosts []string, prefix string, out *[]proxied, hostsBy map[int][]string, depth int) {
	if depth > maxDepth {
		return
	}
	for _, r := range rs {
		h, p := hosts, prefix
		for _, m := range r.Match {
			for _, x := range m.Host {
				h = ingress.AddHost(append([]string(nil), h...), x)
			}
			if len(m.Path) > 0 {
				p = cleanPath(m.Path[0])
			}
		}
		for _, raw := range r.Handle {
			var hd jsonHandler
			if json.Unmarshal(raw, &hd) != nil {
				continue
			}
			switch hd.Handler {
			case "reverse_proxy":
				pr := proxied{path: p}
				for _, u := range hd.Upstreams {
					if u.Dial != "" {
						pr.ups = append(pr.ups, target(u.Dial))
					}
				}
				if hd.LoadBalancing != nil {
					pr.policy = hd.LoadBalancing.SelectionPolicy.Policy
				}
				if len(pr.ups) > 0 {
					hostsBy[len(*out)] = h
					*out = append(*out, pr)
				}
			case "subroute":
				walkJSON(hd.Routes, h, p, out, hostsBy, depth+1)
			}
		}
	}
}
