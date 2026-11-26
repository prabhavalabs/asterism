// Package ingress holds helpers shared by the reverse-proxy config parsers in
// its subpackages (nginx, caddy, haproxy, cloudflared, traefik).
//
// The parsers are pure: they read through a Reader (so the agent can resolve
// paths under a host root and tests can use in-memory files), never open
// TLS keys or tunnel credentials, and emit IngressRoutes with upstream
// addresses only. Resolving an upstream address to a workload is the hub's
// job.
package ingress

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"sort"
	"strings"

	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// MaxFileBytes caps a single config file (hostile or runaway inputs).
const MaxFileBytes = 1 << 20

// MaxFiles caps the number of files one config may include.
const MaxFiles = 256

// MaxRoutes caps the routes one parser returns.
const MaxRoutes = 2000

// ErrTooLarge is returned for files above MaxFileBytes.
var ErrTooLarge = errors.New("ingress: config file exceeds 1 MiB")

// Reader reads host files by absolute path.
type Reader interface {
	ReadFile(name string) ([]byte, error)
	Glob(pattern string) ([]string, error)
}

// MapReader is an in-memory Reader for tests: absolute path -> content.
type MapReader map[string]string

// ReadFile implements Reader.
func (m MapReader) ReadFile(name string) ([]byte, error) {
	s, ok := m[path.Clean(name)]
	if !ok {
		return nil, fmt.Errorf("open %s: %w", name, fs.ErrNotExist)
	}
	if len(s) > MaxFileBytes {
		return nil, ErrTooLarge
	}
	return []byte(s), nil
}

// Glob implements Reader.
func (m MapReader) Glob(pattern string) ([]string, error) {
	var out []string
	for k := range m {
		if ok, err := path.Match(pattern, k); err != nil {
			return nil, err
		} else if ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

// UpstreamAddress normalises a proxy target to "host:port" (or a unix
// socket path, or an upstream/service name) without scheme or path:
//
//	http://127.0.0.1:3000/api -> 127.0.0.1:3000
//	https://backend            -> backend:443
//	localhost:8080             -> localhost:8080
//	unix:/run/app.sock         -> unix:/run/app.sock
func UpstreamAddress(target string) string {
	t := strings.TrimSpace(target)
	if t == "" {
		return ""
	}
	if strings.HasPrefix(t, "unix:") {
		return strings.TrimSuffix(strings.SplitN(t, "|", 2)[0], ":")
	}
	if !strings.Contains(t, "://") {
		if i := strings.IndexByte(t, '/'); i >= 0 {
			t = t[:i]
		}
		return t
	}
	u, err := url.Parse(t)
	if err != nil || u.Host == "" {
		return t
	}
	host := u.Host
	if u.Port() == "" {
		switch u.Scheme {
		case "https", "wss", "grpcs":
			host += ":443"
		case "http", "ws", "grpc", "h2c":
			if !strings.Contains(host, "$") {
				host += ":80"
			}
		}
	}
	return host
}

// RouteID returns "ingress:<kind>/<host><path>" ("/" paths are omitted).
func RouteID(kind, host, pathPrefix string) string {
	id := "ingress:" + kind + "/" + host
	if pathPrefix != "" && pathPrefix != "/" {
		id += pathPrefix
	}
	return id
}

// Strategy names the load-balancing strategy for n upstreams.
func Strategy(policy string, n int) string {
	switch {
	case n <= 1:
		return "single"
	case policy == "":
		return "round_robin"
	default:
		return policy
	}
}

// Finish de-duplicates routes by id (first wins), caps them, and sorts by
// hostname, path and id for stable output.
func Finish(routes []*v1.IngressRoute) []*v1.IngressRoute {
	seen := make(map[string]bool, len(routes))
	out := routes[:0]
	for _, r := range routes {
		if r == nil || seen[r.Id] {
			continue
		}
		seen[r.Id] = true
		out = append(out, r)
		if len(out) >= MaxRoutes {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		hi, hj := firstHost(out[i]), firstHost(out[j])
		if hi != hj {
			return hi < hj
		}
		if out[i].PathPrefix != out[j].PathPrefix {
			return out[i].PathPrefix < out[j].PathPrefix
		}
		return out[i].Id < out[j].Id
	})
	return out
}

func firstHost(r *v1.IngressRoute) string {
	if len(r.Hostnames) > 0 {
		return r.Hostnames[0]
	}
	return ""
}

// AddHost appends h to hosts unless it is empty, a catch-all or a duplicate.
func AddHost(hosts []string, h string) []string {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" || h == "_" || h == "localhost" || strings.HasPrefix(h, "~") {
		return hosts
	}
	for _, x := range hosts {
		if x == h {
			return hosts
		}
	}
	return append(hosts, h)
}
