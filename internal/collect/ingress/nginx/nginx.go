// Package nginx parses nginx configuration into IngressRoutes with a real
// block tokenizer: server, location, upstream and include (with globs) in
// both http and stream contexts.
//
// TLS key files (ssl_certificate_key), auth files and any other referenced
// file are never opened; only the configuration files themselves are read.
package nginx

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/collect/ingress"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Directive is one parsed directive; Block is non-nil for "name args { }".
type Directive struct {
	Name  string
	Args  []string
	Block []*Directive
	File  string
	Line  int
}

// maxDepth bounds block nesting and include recursion.
const maxDepth = 16

// Config is a loaded configuration tree.
type Config struct {
	Root []*Directive
	// Files lists every file read, in order (for change detection).
	Files []string
	// Globs lists include patterns (their directories are watched for new files).
	Globs []string
	// Errors are non-fatal problems (unreadable includes, syntax errors).
	Errors []string
}

// Load reads mainPath and expands include directives. Relative include
// paths resolve against the directory of mainPath (nginx's conf prefix).
func Load(r ingress.Reader, mainPath string) (*Config, error) {
	cfg := &Config{}
	l := &loader{r: r, cfg: cfg, prefix: path.Dir(mainPath), seen: map[string]bool{}}
	root, err := l.file(mainPath, 0)
	if err != nil {
		return nil, err
	}
	cfg.Root = root
	return cfg, nil
}

type loader struct {
	r      ingress.Reader
	cfg    *Config
	prefix string
	seen   map[string]bool
}

func (l *loader) file(p string, depth int) ([]*Directive, error) {
	if len(l.cfg.Files) >= ingress.MaxFiles {
		return nil, fmt.Errorf("more than %d config files", ingress.MaxFiles)
	}
	b, err := l.r.ReadFile(p)
	if err != nil {
		return nil, err
	}
	l.cfg.Files = append(l.cfg.Files, p)
	ds, err := parse(string(b), p)
	if err != nil {
		// nginx refuses to load a file with a syntax error, so nothing in
		// it can be live: report it and skip the whole file.
		l.cfg.Errors = append(l.cfg.Errors, err.Error())
		return nil, nil
	}
	return l.expand(ds, depth)
}

func (l *loader) expand(ds []*Directive, depth int) ([]*Directive, error) {
	if depth > maxDepth {
		return nil, errors.New("include depth exceeded")
	}
	out := make([]*Directive, 0, len(ds))
	for _, d := range ds {
		if d.Name == "include" && d.Block == nil && len(d.Args) == 1 {
			pat := d.Args[0]
			if !path.IsAbs(pat) {
				pat = path.Join(l.prefix, pat)
			}
			var files []string
			if strings.ContainsAny(pat, "*?[") {
				l.cfg.Globs = append(l.cfg.Globs, pat)
				m, err := l.r.Glob(pat)
				if err != nil {
					l.cfg.Errors = append(l.cfg.Errors, fmt.Sprintf("%s:%d: include %s: %v", d.File, d.Line, pat, err))
				}
				files = m
			} else {
				files = []string{pat}
			}
			for _, f := range files {
				if l.seen[f] {
					continue // include cycles
				}
				l.seen[f] = true
				sub, err := l.file(f, depth+1)
				if err != nil {
					l.cfg.Errors = append(l.cfg.Errors, fmt.Sprintf("%s:%d: include %s: %v", d.File, d.Line, f, err))
					continue
				}
				out = append(out, sub...)
			}
			continue
		}
		if d.Block != nil {
			b, err := l.expand(d.Block, depth+1)
			if err != nil {
				return nil, err
			}
			d.Block = b
		}
		out = append(out, d)
	}
	return out, nil
}

// parse tokenizes and builds the directive tree of one file.
func parse(src, file string) ([]*Directive, error) {
	t := &tokenizer{src: src, line: 1}
	ds, err := t.block(0, file)
	if err == nil && !t.eof() {
		err = fmt.Errorf("%s:%d: unexpected '}'", file, t.line)
	}
	return ds, err
}

type tokenizer struct {
	src  string
	pos  int
	line int
}

func (t *tokenizer) eof() bool { return t.pos >= len(t.src) }

// next returns the next token: a word, "{", "}" or ";".
func (t *tokenizer) next() (tok string, quoted bool, ok bool) {
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		switch {
		case c == '\n':
			t.line++
			t.pos++
		case c == ' ' || c == '\t' || c == '\r':
			t.pos++
		case c == '#':
			for t.pos < len(t.src) && t.src[t.pos] != '\n' {
				t.pos++
			}
		default:
			goto token
		}
	}
	return "", false, false
token:
	c := t.src[t.pos]
	switch c {
	case '{', '}', ';':
		t.pos++
		return string(c), false, true
	case '"', '\'':
		q := c
		t.pos++
		var b strings.Builder
		for t.pos < len(t.src) && t.src[t.pos] != q {
			if t.src[t.pos] == '\\' && t.pos+1 < len(t.src) {
				t.pos++
			}
			if t.src[t.pos] == '\n' {
				t.line++
			}
			b.WriteByte(t.src[t.pos])
			t.pos++
		}
		t.pos++ // closing quote
		return b.String(), true, true
	}
	start := t.pos
	for t.pos < len(t.src) {
		c := t.src[t.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ';' || c == '{' || c == '}' {
			break
		}
		if c == '\\' && t.pos+1 < len(t.src) {
			t.pos++
		}
		t.pos++
	}
	return t.src[start:t.pos], false, true
}

func (t *tokenizer) block(depth int, file string) ([]*Directive, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%s:%d: blocks nested too deeply", file, t.line)
	}
	var out []*Directive
	var cur *Directive
	for {
		tok, quoted, ok := t.next()
		if !ok {
			if cur != nil {
				return out, fmt.Errorf("%s:%d: unexpected end of file, expecting ';' or '}'", file, t.line)
			}
			if depth > 0 {
				return out, fmt.Errorf("%s:%d: unexpected end of file, expecting '}'", file, t.line)
			}
			return out, nil
		}
		switch {
		case tok == ";" && !quoted:
			if cur != nil {
				out = append(out, cur)
				cur = nil
			}
		case tok == "{" && !quoted:
			if cur == nil {
				cur = &Directive{File: file, Line: t.line}
			}
			body, err := t.block(depth+1, file)
			cur.Block = body
			if cur.Block == nil {
				cur.Block = []*Directive{}
			}
			out = append(out, cur)
			cur = nil
			if err != nil {
				return out, err
			}
		case tok == "}" && !quoted:
			if depth == 0 {
				t.pos-- // let parse report it
				return out, fmt.Errorf("%s:%d: unexpected '}'", file, t.line)
			}
			if cur != nil {
				out = append(out, cur)
			}
			return out, nil
		default:
			if cur == nil {
				cur = &Directive{Name: tok, File: file, Line: t.line}
			} else {
				cur.Args = append(cur.Args, tok)
			}
		}
	}
}

// Routes loads mainPath and returns its proxied routes. proxyWorkloadID is
// set on every route (e.g. "unit:nginx.service"; "" when unknown).
func Routes(r ingress.Reader, mainPath, proxyWorkloadID string) ([]*v1.IngressRoute, *Config, error) {
	cfg, err := Load(r, mainPath)
	if err != nil {
		return nil, nil, err
	}
	return Extract(cfg.Root, proxyWorkloadID), cfg, nil
}

type upstream struct {
	servers []*v1.UpstreamTarget
	policy  string
}

// Extract builds routes from a directive tree.
func Extract(root []*Directive, proxyWorkloadID string) []*v1.IngressRoute {
	var routes []*v1.IngressRoute
	for _, ctx := range root {
		if ctx.Block == nil || (ctx.Name != "http" && ctx.Name != "stream") {
			continue
		}
		ups := map[string]*upstream{}
		for _, d := range ctx.Block {
			if d.Name == "upstream" && d.Block != nil && len(d.Args) == 1 {
				ups[d.Args[0]] = parseUpstream(d.Block)
			}
		}
		for _, d := range ctx.Block {
			if d.Name == "server" && d.Block != nil {
				routes = append(routes, serverRoutes(d.Block, ups, ctx.Name == "stream", proxyWorkloadID)...)
			}
		}
	}
	return ingress.Finish(routes)
}

func parseUpstream(block []*Directive) *upstream {
	u := &upstream{}
	for _, d := range block {
		switch d.Name {
		case "server":
			if len(d.Args) == 0 {
				continue
			}
			t := &v1.UpstreamTarget{Address: ingress.UpstreamAddress(d.Args[0]), Weight: 1, Healthy: true}
			for _, a := range d.Args[1:] {
				switch {
				case strings.HasPrefix(a, "weight="):
					if w, err := strconv.Atoi(strings.TrimPrefix(a, "weight=")); err == nil && w > 0 {
						t.Weight = uint32(w)
					}
				case a == "backup":
					t.Backup = true
				case a == "down":
					t.Healthy = false
				}
			}
			u.servers = append(u.servers, t)
		case "least_conn", "ip_hash", "random", "least_time":
			u.policy = d.Name
		case "hash":
			u.policy = "hash"
		}
	}
	return u
}

type location struct {
	path   string
	target string
}

func serverRoutes(block []*Directive, ups map[string]*upstream, stream bool, proxyID string) []*v1.IngressRoute {
	var hosts []string
	var port uint32
	tls := false
	var locs []location
	for _, d := range block {
		switch d.Name {
		case "server_name":
			for _, h := range d.Args {
				hosts = ingress.AddHost(hosts, h)
			}
		case "listen":
			p, ssl := parseListen(d.Args)
			if ssl {
				tls = true
			}
			if p != 0 && (port == 0 || (ssl && p == 443)) {
				port = p
			}
		case "ssl_certificate":
			tls = true // the path is noted, the file is never opened
		case "ssl":
			if len(d.Args) == 1 && d.Args[0] == "on" {
				tls = true
			}
		case "proxy_pass", "grpc_pass", "fastcgi_pass", "uwsgi_pass", "scgi_pass":
			if len(d.Args) > 0 {
				locs = append(locs, location{path: "/", target: passTarget(d)})
			}
		case "location":
			collectLocations(d, &locs, 0)
		}
	}
	if port == 0 {
		port = 80
		if tls {
			port = 443
		}
	}
	var out []*v1.IngressRoute
	for _, loc := range locs {
		targets, policy := resolve(loc.target, ups)
		if len(targets) == 0 {
			continue
		}
		key := ""
		if len(hosts) > 0 {
			key = hosts[0]
		} else {
			key = ":" + strconv.Itoa(int(port))
		}
		pathPrefix := loc.path
		if stream {
			pathPrefix = ""
		}
		out = append(out, &v1.IngressRoute{
			Id:              ingress.RouteID("nginx", key, pathPrefix),
			ProxyKind:       v1.ProxyKind_PROXY_KIND_NGINX,
			ProxyWorkloadId: proxyID,
			Hostnames:       append([]string(nil), hosts...),
			ListenPort:      port,
			Tls:             tls,
			PathPrefix:      pathPrefix,
			Strategy:        ingress.Strategy(policy, len(targets)),
			Upstreams:       targets,
		})
	}
	return out
}

// passTarget returns the *_pass target ("http://backend",
// "127.0.0.1:9000", "unix:/run/php.sock" or an upstream name).
func passTarget(d *Directive) string { return d.Args[0] }

func collectLocations(d *Directive, locs *[]location, depth int) {
	if d.Block == nil || depth > maxDepth {
		return
	}
	p := locationPath(d.Args)
	for _, c := range d.Block {
		switch c.Name {
		case "proxy_pass", "grpc_pass", "fastcgi_pass", "uwsgi_pass", "scgi_pass":
			if len(c.Args) > 0 && p != "" {
				*locs = append(*locs, location{path: p, target: passTarget(c)})
			}
		case "location":
			collectLocations(c, locs, depth+1)
		}
	}
}

// locationPath returns the literal prefix of a location ("" for regex and
// named locations, which have no stable prefix).
func locationPath(args []string) string {
	switch len(args) {
	case 1:
		if strings.HasPrefix(args[0], "@") {
			return ""
		}
		return args[0]
	case 2:
		switch args[0] {
		case "=", "^~":
			return args[1]
		}
	}
	return ""
}

func resolve(target string, ups map[string]*upstream) ([]*v1.UpstreamTarget, string) {
	name := target
	if i := strings.Index(name, "://"); i >= 0 {
		name = name[i+3:]
	}
	if i := strings.IndexByte(name, '/'); i >= 0 {
		name = name[:i]
	}
	if u, ok := ups[name]; ok {
		out := make([]*v1.UpstreamTarget, 0, len(u.servers))
		for _, s := range u.servers {
			out = append(out, proto.Clone(s).(*v1.UpstreamTarget))
		}
		return out, u.policy
	}
	addr := ingress.UpstreamAddress(target)
	if addr == "" {
		return nil, ""
	}
	return []*v1.UpstreamTarget{{Address: addr, Weight: 1, Healthy: true}}, ""
}

// parseListen returns the port and whether "ssl" is set. Unix sockets
// return port 0.
func parseListen(args []string) (uint32, bool) {
	if len(args) == 0 {
		return 0, false
	}
	ssl := false
	for _, a := range args[1:] {
		if a == "ssl" || a == "quic" {
			ssl = true
		}
	}
	a := args[0]
	if strings.HasPrefix(a, "unix:") {
		return 0, ssl
	}
	if i := strings.LastIndexByte(a, ':'); i >= 0 && !strings.HasSuffix(a, "]") {
		a = a[i+1:]
	}
	p, err := strconv.Atoi(a)
	if err != nil || p <= 0 || p > 65535 {
		return 0, ssl // "listen example.com;" means port 80
	}
	return uint32(p), ssl
}
