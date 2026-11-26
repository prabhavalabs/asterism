// Package caddy parses Caddy configuration into IngressRoutes: the
// Caddyfile (site blocks, snippets, imports, reverse_proxy inside handle,
// handle_path and route) and the admin API's JSON config (GET /config/).
package caddy

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/prabhavalabs/asterism/internal/collect/ingress"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

const maxDepth = 16

// token is one Caddyfile token; nl reports a newline before it.
type token struct {
	text   string
	quoted bool
	nl     bool
	line   int
}

func tokenize(src string) []token {
	var out []token
	line, nl := 1, true
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n':
			line++
			nl = true
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r':
			i++
			continue
		case c == '#' && (i == 0 || src[i-1] == ' ' || src[i-1] == '\t' || src[i-1] == '\n'):
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case c == '"' || c == '`':
			q := c
			i++
			var b strings.Builder
			for i < len(src) && src[i] != q {
				if q == '"' && src[i] == '\\' && i+1 < len(src) && (src[i+1] == '"' || src[i+1] == '\\') {
					i++
				}
				if src[i] == '\n' {
					line++
				}
				b.WriteByte(src[i])
				i++
			}
			i++
			out = append(out, token{text: b.String(), quoted: true, nl: nl, line: line})
			nl = false
			continue
		}
		start := i
		for i < len(src) && src[i] != ' ' && src[i] != '\t' && src[i] != '\n' && src[i] != '\r' {
			i++
		}
		out = append(out, token{text: src[start:i], nl: nl, line: line})
		nl = false
	}
	return out
}

// node is a directive line with an optional block.
type node struct {
	args  []string
	block []*node
	line  int
}

func (n *node) name() string {
	if len(n.args) == 0 {
		return ""
	}
	return n.args[0]
}

// parseNodes groups tokens into lines and blocks.
func parseNodes(toks []token, pos *int, depth int) ([]*node, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("blocks nested too deeply")
	}
	var out []*node
	var cur *node
	for *pos < len(toks) {
		t := toks[*pos]
		if t.nl && cur != nil {
			out = append(out, cur)
			cur = nil
		}
		*pos++
		switch {
		case !t.quoted && t.text == "{":
			if cur == nil {
				cur = &node{line: t.line}
			}
			b, err := parseNodes(toks, pos, depth+1)
			if err != nil {
				return nil, err
			}
			cur.block = b
			if cur.block == nil {
				cur.block = []*node{}
			}
			out = append(out, cur)
			cur = nil
		case !t.quoted && t.text == "}":
			if depth == 0 {
				return nil, fmt.Errorf("line %d: unexpected '}'", t.line)
			}
			if cur != nil {
				out = append(out, cur)
			}
			return out, nil
		default:
			if cur == nil {
				cur = &node{line: t.line}
			}
			cur.args = append(cur.args, t.text)
		}
	}
	if depth > 0 {
		return nil, fmt.Errorf("unexpected end of file, expecting '}'")
	}
	if cur != nil {
		out = append(out, cur)
	}
	return out, nil
}

// Caddyfile is a loaded Caddyfile.
type Caddyfile struct {
	Files  []string
	Errors []string
	sites  []*node
}

// LoadCaddyfile reads a Caddyfile, expanding import of files and globs
// (relative to its directory) and of snippets.
func LoadCaddyfile(r ingress.Reader, p string) (*Caddyfile, error) {
	cf := &Caddyfile{}
	snippets := map[string][]*node{}
	top, err := loadNodes(r, p, cf, 0)
	if err != nil {
		return nil, err
	}
	// First pass: snippets.
	var rest []*node
	for _, n := range top {
		name := n.name()
		if n.block != nil && len(n.args) == 1 && strings.HasPrefix(name, "(") && strings.HasSuffix(name, ")") {
			snippets[strings.Trim(name, "()")] = n.block
			continue
		}
		rest = append(rest, n)
	}
	rest = expandImports(rest, snippets, r, path.Dir(p), cf, 0)
	// A leading block without arguments is the global options block. A
	// Caddyfile with a single site may omit braces: then the first line is
	// the address and the remaining lines are its directives.
	if len(rest) > 0 && len(rest[0].args) == 0 && rest[0].block != nil {
		rest = rest[1:]
	}
	if len(rest) > 0 && rest[0].block == nil && looksLikeAddress(rest[0].name()) {
		site := &node{args: rest[0].args, block: rest[1:], line: rest[0].line}
		rest = []*node{site}
	}
	for _, n := range rest {
		if n.block != nil && len(n.args) > 0 {
			cf.sites = append(cf.sites, n)
		}
	}
	return cf, nil
}

func loadNodes(r ingress.Reader, p string, cf *Caddyfile, depth int) ([]*node, error) {
	if len(cf.Files) >= ingress.MaxFiles {
		return nil, fmt.Errorf("more than %d files", ingress.MaxFiles)
	}
	b, err := r.ReadFile(p)
	if err != nil {
		return nil, err
	}
	cf.Files = append(cf.Files, p)
	toks := tokenize(string(b))
	pos := 0
	ns, err := parseNodes(toks, &pos, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return ns, nil
}

func expandImports(ns []*node, snippets map[string][]*node, r ingress.Reader, dir string, cf *Caddyfile, depth int) []*node {
	if depth > maxDepth {
		return ns
	}
	var out []*node
	for _, n := range ns {
		if n.name() == "import" && n.block == nil && len(n.args) >= 2 {
			target := n.args[1]
			if sn, ok := snippets[target]; ok {
				out = append(out, expandImports(sn, snippets, r, dir, cf, depth+1)...)
				continue
			}
			pat := target
			if !path.IsAbs(pat) {
				pat = path.Join(dir, pat)
			}
			files := []string{pat}
			if strings.ContainsAny(pat, "*?[") {
				files, _ = r.Glob(pat)
			}
			for _, f := range files {
				sub, err := loadNodes(r, f, cf, depth+1)
				if err != nil {
					cf.Errors = append(cf.Errors, fmt.Sprintf("import %s: %v", f, err))
					continue
				}
				out = append(out, expandImports(sub, snippets, r, path.Dir(f), cf, depth+1)...)
			}
			continue
		}
		if n.block != nil {
			n.block = expandImports(n.block, snippets, r, dir, cf, depth+1)
		}
		out = append(out, n)
	}
	return out
}

func looksLikeAddress(s string) bool {
	if strings.Contains(s, "://") || strings.HasPrefix(s, ":") {
		return true
	}
	h := s
	if i := strings.LastIndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return strings.Contains(h, ".") || h == "localhost"
}

// address is one parsed site address.
type address struct {
	host string
	port uint32
	tls  bool
}

func parseAddress(a string) address {
	a = strings.TrimSuffix(strings.TrimSpace(a), ",")
	scheme := ""
	if i := strings.Index(a, "://"); i >= 0 {
		scheme, a = a[:i], a[i+3:]
	}
	if i := strings.IndexByte(a, '/'); i >= 0 {
		a = a[:i]
	}
	host, port := a, ""
	if i := strings.LastIndexByte(a, ':'); i >= 0 && !strings.HasSuffix(a, "]") {
		host, port = a[:i], a[i+1:]
	}
	ad := address{host: strings.ToLower(host)}
	if p, err := strconv.Atoi(port); err == nil && p > 0 && p < 65536 {
		ad.port = uint32(p)
	}
	switch {
	case scheme == "http":
		ad.tls = false
	case scheme == "https":
		ad.tls = true
	case ad.port == 80:
		ad.tls = false
	case ad.host == "" || ad.host == "localhost" || strings.HasPrefix(ad.host, "127.") || isIPLiteral(ad.host):
		ad.tls = ad.port == 443
	default:
		ad.tls = true // automatic HTTPS for named hosts
	}
	if ad.port == 0 {
		ad.port = 80
		if ad.tls {
			ad.port = 443
		}
	}
	return ad
}

func isIPLiteral(h string) bool {
	if strings.Contains(h, ":") {
		return true
	}
	for _, c := range h {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return h != ""
}

// Routes returns the Caddyfile's proxied routes.
func (cf *Caddyfile) Routes(proxyWorkloadID string) []*v1.IngressRoute {
	var routes []*v1.IngressRoute
	for _, site := range cf.sites {
		var hosts []string
		var port uint32
		tls := false
		for _, a := range strings.Split(strings.Join(site.args, " "), ",") {
			for _, f := range strings.Fields(a) {
				ad := parseAddress(f)
				hosts = ingress.AddHost(hosts, ad.host)
				if port == 0 || ad.tls {
					port, tls = ad.port, ad.tls
				}
			}
		}
		matchers := collectMatchers(site.block)
		var prox []proxied
		walk(site.block, "/", matchers, &prox, 0)
		for _, p := range prox {
			key := ":" + strconv.Itoa(int(port))
			if len(hosts) > 0 {
				key = hosts[0]
			}
			routes = append(routes, &v1.IngressRoute{
				Id:              ingress.RouteID("caddy", key, p.path),
				ProxyKind:       v1.ProxyKind_PROXY_KIND_CADDY,
				ProxyWorkloadId: proxyWorkloadID,
				Hostnames:       append([]string(nil), hosts...),
				ListenPort:      port,
				Tls:             tls,
				PathPrefix:      p.path,
				Strategy:        ingress.Strategy(p.policy, len(p.ups)),
				Upstreams:       p.ups,
			})
		}
	}
	return ingress.Finish(routes)
}

type proxied struct {
	path   string
	ups    []*v1.UpstreamTarget
	policy string
}

// collectMatchers maps "@name" to its path prefix (path matchers only).
func collectMatchers(block []*node) map[string]string {
	m := map[string]string{}
	for _, n := range block {
		name := n.name()
		if !strings.HasPrefix(name, "@") {
			continue
		}
		if len(n.args) >= 3 && n.args[1] == "path" {
			m[name] = n.args[2]
		}
		for _, c := range n.block {
			if c.name() == "path" && len(c.args) >= 2 {
				m[name] = c.args[1]
			}
		}
	}
	return m
}

// cleanPath turns a matcher such as "/api/*" into the prefix "/api/".
func cleanPath(p string) string {
	p = strings.TrimSuffix(p, "*")
	if p == "" {
		return "/"
	}
	return p
}

func walk(block []*node, prefix string, matchers map[string]string, out *[]proxied, depth int) {
	if depth > maxDepth {
		return
	}
	for _, n := range block {
		switch n.name() {
		case "reverse_proxy":
			args := n.args[1:]
			p := prefix
			if len(args) > 0 {
				switch a := args[0]; {
				case strings.HasPrefix(a, "@"):
					if mp, ok := matchers[a]; ok {
						p = cleanPath(mp)
					}
					args = args[1:]
				case strings.HasPrefix(a, "/") || a == "*":
					if a != "*" {
						p = cleanPath(a)
					}
					args = args[1:]
				}
			}
			pr := proxied{path: p}
			for _, a := range args {
				pr.ups = append(pr.ups, target(a))
			}
			for _, c := range n.block {
				switch c.name() {
				case "to":
					for _, a := range c.args[1:] {
						pr.ups = append(pr.ups, target(a))
					}
				case "lb_policy":
					if len(c.args) > 1 {
						pr.policy = c.args[1]
					}
				}
			}
			if len(pr.ups) > 0 {
				*out = append(*out, pr)
			}
		case "handle", "handle_path", "route":
			p := prefix
			if len(n.args) > 1 {
				a := n.args[1]
				if mp, ok := matchers[a]; ok {
					a = mp
				}
				if strings.HasPrefix(a, "/") {
					p = cleanPath(a)
				}
			}
			walk(n.block, p, matchers, out, depth+1)
		}
	}
}

func target(a string) *v1.UpstreamTarget {
	a = strings.TrimPrefix(a, "h2c://")
	if strings.HasPrefix(a, "unix/") {
		a = "unix:" + strings.TrimPrefix(a, "unix/")
	}
	return &v1.UpstreamTarget{Address: ingress.UpstreamAddress(a), Weight: 1, Healthy: true}
}
