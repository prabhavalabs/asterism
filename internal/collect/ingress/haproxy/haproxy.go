// Package haproxy parses haproxy.cfg frontends, backends and listen sections
// into IngressRoutes. Host routing comes from ACLs on hdr(host),
// hdr_dom(host), req.hdr(host) and ssl_fc_sni used by use_backend rules.
//
// Routes carry PROXY_KIND_HAPROXY and the "ingress:haproxy/..." id prefix.
package haproxy

import (
	"bufio"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/collect/ingress"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

type section struct {
	kind, name string
	binds      []bind
	acls       map[string][]string // acl name -> hostnames
	uses       []use
	defBackend string
	servers    []*v1.UpstreamTarget
	balance    string
}

type bind struct {
	port uint32
	tls  bool
}

type use struct {
	backend string
	acls    []string
}

// Parse parses one haproxy.cfg.
func Parse(src []byte, proxyWorkloadID string) []*v1.IngressRoute {
	var secs []*section
	var cur *section
	sc := bufio.NewScanner(strings.NewReader(string(src)))
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "global", "defaults", "frontend", "backend", "listen", "peers", "resolvers", "userlist", "cache", "program", "mailers", "http-errors", "ring":
			cur = &section{kind: f[0], acls: map[string][]string{}}
			if len(f) > 1 {
				cur.name = f[1]
			}
			if cur.kind == "listen" && len(f) > 2 {
				cur.binds = append(cur.binds, parseBind(f[2:]))
			}
			secs = append(secs, cur)
			continue
		}
		if cur == nil {
			continue
		}
		switch cur.kind {
		case "frontend", "listen", "backend":
		default:
			continue
		}
		switch f[0] {
		case "bind":
			if len(f) > 1 {
				cur.binds = append(cur.binds, parseBind(f[1:]))
			}
		case "acl":
			if len(f) >= 4 && isHostFetch(f[2]) {
				for _, v := range f[3:] {
					if strings.HasPrefix(v, "-") {
						continue // -i, -m dom ...
					}
					cur.acls[f[1]] = ingress.AddHost(cur.acls[f[1]], v)
				}
			}
		case "use_backend":
			if len(f) >= 2 {
				u := use{backend: f[1]}
				for _, w := range f[2:] {
					w = strings.TrimPrefix(w, "!")
					if w != "if" && w != "unless" && w != "||" && w != "or" && w != "{" && w != "}" {
						u.acls = append(u.acls, w)
					}
				}
				cur.uses = append(cur.uses, u)
			}
		case "default_backend":
			if len(f) >= 2 {
				cur.defBackend = f[1]
			}
		case "balance":
			if len(f) >= 2 {
				cur.balance = f[1]
			}
		case "server":
			if len(f) >= 3 {
				t := &v1.UpstreamTarget{Address: ingress.UpstreamAddress(f[2]), Weight: 1, Healthy: true}
				for i := 3; i < len(f); i++ {
					switch f[i] {
					case "backup":
						t.Backup = true
					case "disabled":
						t.Healthy = false
					case "weight":
						if i+1 < len(f) {
							if w, err := strconv.Atoi(f[i+1]); err == nil && w >= 0 {
								t.Weight = uint32(w)
							}
						}
					}
				}
				cur.servers = append(cur.servers, t)
			}
		}
	}
	backends := map[string]*section{}
	for _, s := range secs {
		if s.kind == "backend" || s.kind == "listen" {
			backends[s.name] = s
		}
	}
	var routes []*v1.IngressRoute
	for _, s := range secs {
		if s.kind != "frontend" && s.kind != "listen" {
			continue
		}
		port, tls := frontPort(s.binds)
		mk := func(be *section, hosts []string, key string) {
			if be == nil || len(be.servers) == 0 {
				return
			}
			ups := make([]*v1.UpstreamTarget, len(be.servers))
			for i, t := range be.servers {
				ups[i] = proto.Clone(t).(*v1.UpstreamTarget)
			}
			routes = append(routes, &v1.IngressRoute{
				Id:              ingress.RouteID("haproxy", key, ""),
				ProxyKind:       v1.ProxyKind_PROXY_KIND_HAPROXY,
				ProxyWorkloadId: proxyWorkloadID,
				Hostnames:       hosts,
				ListenPort:      port,
				Tls:             tls,
				PathPrefix:      "/",
				Strategy:        ingress.Strategy(be.balance, len(ups)),
				Upstreams:       ups,
			})
		}
		if s.kind == "listen" {
			mk(s, nil, s.name)
			continue
		}
		for _, u := range s.uses {
			var hosts []string
			for _, a := range u.acls {
				for _, h := range s.acls[a] {
					hosts = ingress.AddHost(hosts, h)
				}
			}
			key := s.name + ":" + u.backend
			if len(hosts) > 0 {
				key = hosts[0]
			}
			mk(backends[u.backend], hosts, key)
		}
		if s.defBackend != "" {
			mk(backends[s.defBackend], nil, s.name)
		}
	}
	return ingress.Finish(routes)
}

func isHostFetch(f string) bool {
	f = strings.ToLower(f)
	return strings.HasPrefix(f, "hdr(host") || strings.HasPrefix(f, "hdr_dom(host") || strings.HasPrefix(f, "hdr_beg(host") ||
		strings.HasPrefix(f, "hdr_end(host") || strings.HasPrefix(f, "req.hdr(host") || strings.HasPrefix(f, "ssl_fc_sni") || strings.HasPrefix(f, "req.ssl_sni")
}

func parseBind(f []string) bind {
	var b bind
	addr := f[0]
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		p := addr[i+1:]
		if j := strings.IndexByte(p, ','); j >= 0 {
			p = p[:j]
		}
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
			b.port = uint32(n)
		}
	}
	for _, x := range f[1:] {
		if x == "ssl" {
			b.tls = true
		}
	}
	return b
}

func frontPort(bs []bind) (uint32, bool) {
	var port uint32
	tls := false
	for _, b := range bs {
		if b.tls {
			tls = true
			port = b.port
		} else if port == 0 {
			port = b.port
		}
	}
	return port, tls
}
