package nginx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/collect/ingress"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// loadTree maps testdata/<path> to /<path>.
func loadTree(t testing.TB, dir string) ingress.MapReader {
	t.Helper()
	m := ingress.MapReader{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		m["/"+filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRoutes(t *testing.T) {
	r := loadTree(t, "testdata")
	routes, cfg, err := Routes(r, "/etc/nginx/nginx.conf", "unit:nginx.service")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Errors) != 2 || !strings.Contains(strings.Join(cfg.Errors, "\n"), "broken.conf") {
		// modules-enabled/*.conf matches nothing (not an error); broken.conf is.
		t.Logf("errors: %q", cfg.Errors)
	}
	if !strings.Contains(strings.Join(cfg.Errors, "\n"), "broken.conf") {
		t.Fatalf("broken.conf syntax error not reported: %q", cfg.Errors)
	}
	byID := map[string]*v1.IngressRoute{}
	for _, rt := range routes {
		byID[rt.Id] = rt
		if rt.ProxyWorkloadId != "unit:nginx.service" || rt.ProxyKind != v1.ProxyKind_PROXY_KIND_NGINX {
			t.Errorf("%s: proxy %q %v", rt.Id, rt.ProxyWorkloadId, rt.ProxyKind)
		}
		b, _ := proto.Marshal(rt)
		if strings.Contains(string(b), "s3cr3t") || strings.Contains(string(b), "privkey") {
			t.Fatalf("%s leaks a secret or key path", rt.Id)
		}
	}
	type want struct {
		hosts    string
		port     uint32
		tls      bool
		path     string
		strategy string
		ups      string
	}
	cases := map[string]want{
		"ingress:nginx/shop.example.com":            {"shop.example.com,www.shop.example.com", 443, true, "/", "single", "127.0.0.1:3000"},
		"ingress:nginx/shop.example.com/api/":       {"shop.example.com,www.shop.example.com", 443, true, "/api/", "least_conn", "127.0.0.1:3001*3,127.0.0.1:3002*1,10.0.0.5:3001*1(backup)"},
		"ingress:nginx/shop.example.com/api/admin/": {"shop.example.com,www.shop.example.com", 443, true, "/api/admin/", "single", "127.0.0.1:3100"},
		"ingress:nginx/:8081/grafana/":              {"", 8081, false, "/grafana/", "single", "localhost:3005"},
		"ingress:nginx/:8081/php":                   {"", 8081, false, "/php", "single", "unix:/run/php/php8.3-fpm.sock"},
		"ingress:nginx/api.example.com":             {"api.example.com", 443, true, "/", "single", "127.0.0.1:50051"},
		"ingress:nginx/:5433":                       {"", 5433, false, "", "single", "127.0.0.1:5432"},
	}
	for id, w := range cases {
		rt, ok := byID[id]
		if !ok {
			t.Errorf("missing route %s (have %v)", id, keys(byID))
			continue
		}
		var ups []string
		for _, u := range rt.Upstreams {
			s := u.Address
			if len(rt.Upstreams) > 1 {
				s += "*" + string(rune('0'+u.Weight))
			}
			if u.Backup {
				s += "(backup)"
			}
			ups = append(ups, s)
		}
		got := want{strings.Join(rt.Hostnames, ","), rt.ListenPort, rt.Tls, rt.PathPrefix, rt.Strategy, strings.Join(ups, ",")}
		if got != w {
			t.Errorf("%s:\n got %+v\nwant %+v", id, got, w)
		}
	}
	if len(routes) != len(cases) {
		t.Errorf("got %d routes, want %d: %v", len(routes), len(cases), keys(byID))
	}
	if _, ok := byID["ingress:nginx/broken.example.com"]; ok {
		t.Error("route from a file with a syntax error")
	}
	if len(cfg.Files) != 7 || len(cfg.Globs) != 3 {
		t.Errorf("files %v globs %v", cfg.Files, cfg.Globs)
	}
}

func keys(m map[string]*v1.IngressRoute) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestTokenizer(t *testing.T) {
	ds, err := parse(`a "b c" 'd;e' f\;g { h; i { j k; } } # x { ;
l;`, "t")
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 || ds[0].Name != "a" || strings.Join(ds[0].Args, "|") != `b c|d;e|f\;g` || len(ds[0].Block) != 2 || ds[0].Block[1].Block[0].Args[0] != "k" || ds[1].Name != "l" || ds[1].Line != 2 {
		t.Fatalf("%+v", ds)
	}
	for _, bad := range []string{"a {", "a }", "a", "a { b; } }"} {
		if _, err := parse(bad, "t"); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestIncludeCycleAndCaps(t *testing.T) {
	r := ingress.MapReader{
		"/etc/nginx/nginx.conf": "http { include /etc/nginx/a.conf; }",
		"/etc/nginx/a.conf":     "include /etc/nginx/b.conf; server { listen 80; server_name a.test; location / { proxy_pass http://127.0.0.1:1; } }",
		"/etc/nginx/b.conf":     "include /etc/nginx/a.conf;",
	}
	routes, _, err := Routes(r, "/etc/nginx/nginx.conf", "")
	if err != nil || len(routes) != 1 {
		t.Fatalf("%v %v", routes, err)
	}
	if _, _, err := Routes(r, "/etc/nginx/missing.conf", ""); err == nil {
		t.Fatal("missing main config")
	}
	r["/etc/nginx/huge.conf"] = strings.Repeat("#", ingress.MaxFileBytes+1)
	if _, _, err := Routes(r, "/etc/nginx/huge.conf", ""); err == nil {
		t.Fatal("oversized config accepted")
	}
	deep := strings.Repeat("a {", 40) + strings.Repeat("}", 40)
	if _, err := parse(deep, "t"); err == nil {
		t.Fatal("deep nesting accepted")
	}
}

func TestParseListen(t *testing.T) {
	for in, want := range map[string]uint32{"80": 80, "443 ssl": 443, "[::]:443 ssl": 443, "127.0.0.1:8080": 8080, "unix:/run/x.sock": 0, "example.com": 0, "*:8443 quic": 8443} {
		p, _ := parseListen(strings.Fields(in))
		if p != want {
			t.Errorf("%s: %d want %d", in, p, want)
		}
	}
}

func BenchmarkRoutes(b *testing.B) {
	r := loadTree(b, "testdata")
	b.ReportAllocs()
	for b.Loop() {
		_, _, _ = Routes(r, "/etc/nginx/nginx.conf", "")
	}
}
