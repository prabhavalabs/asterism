package caddy

import (
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/collect/ingress"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func reader(t *testing.T) ingress.MapReader {
	t.Helper()
	m := ingress.MapReader{}
	for _, p := range []string{"etc/caddy/Caddyfile", "etc/caddy/sites/grafana.caddy"} {
		b, err := os.ReadFile("testdata/" + p)
		if err != nil {
			t.Fatal(err)
		}
		m["/"+p] = string(b)
	}
	return m
}

type row struct {
	hosts    string
	port     uint32
	tls      bool
	strategy string
	ups      string
}

func summarize(t *testing.T, routes []*v1.IngressRoute) map[string]row {
	t.Helper()
	out := map[string]row{}
	for _, r := range routes {
		b, _ := proto.Marshal(r)
		if strings.Contains(string(b), "s3cr3t") {
			t.Fatalf("%s leaks a secret", r.Id)
		}
		if r.ProxyKind != v1.ProxyKind_PROXY_KIND_CADDY {
			t.Fatalf("%s kind %v", r.Id, r.ProxyKind)
		}
		var ups []string
		for _, u := range r.Upstreams {
			ups = append(ups, u.Address)
		}
		out[r.Id] = row{strings.Join(r.Hostnames, ","), r.ListenPort, r.Tls, r.Strategy, strings.Join(ups, ",")}
	}
	return out
}

func TestCaddyfile(t *testing.T) {
	cf, err := LoadCaddyfile(reader(t), "/etc/caddy/Caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	if len(cf.Files) != 2 || len(cf.Errors) != 0 {
		t.Fatalf("files %v errors %v", cf.Files, cf.Errors)
	}
	got := summarize(t, cf.Routes("unit:caddy.service"))
	want := map[string]row{
		"ingress:caddy/shop.example.com/api/":   {"shop.example.com,www.shop.example.com", 443, true, "least_conn", "127.0.0.1:3001,127.0.0.1:3002"},
		"ingress:caddy/shop.example.com/admin/": {"shop.example.com,www.shop.example.com", 443, true, "single", "127.0.0.1:3100"},
		"ingress:caddy/shop.example.com":        {"shop.example.com,www.shop.example.com", 443, true, "single", "localhost:3000"},
		"ingress:caddy/status.example.com":      {"status.example.com", 80, false, "round_robin", "127.0.0.1:3005,unix:/run/kuma.sock"},
		"ingress:caddy/:8080/metrics":           {"", 8080, false, "single", "127.0.0.1:9100"},
		"ingress:caddy/grafana.example.com":     {"grafana.example.com", 443, true, "single", "grafana:3000"},
	}
	compare(t, got, want)
}

func compare(t *testing.T, got, want map[string]row) {
	t.Helper()
	for id, w := range want {
		if g, ok := got[id]; !ok {
			t.Errorf("missing %s (have %v)", id, got)
		} else if g != w {
			t.Errorf("%s:\n got %+v\nwant %+v", id, g, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d routes want %d: %v", len(got), len(want), got)
	}
}

func TestSingleSiteNoBraces(t *testing.T) {
	r := ingress.MapReader{"/Caddyfile": "localhost:8443\n\nreverse_proxy 127.0.0.1:9000\n"}
	cf, err := LoadCaddyfile(r, "/Caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	got := summarize(t, cf.Routes(""))
	compare(t, got, map[string]row{"ingress:caddy/:8443": {"", 8443, false, "single", "127.0.0.1:9000"}})
	for _, bad := range []string{"a.com {\n reverse_proxy x\n", "}\n"} {
		if _, err := LoadCaddyfile(ingress.MapReader{"/C": bad}, "/C"); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestAdminJSON(t *testing.T) {
	raw, err := os.ReadFile("testdata/admin-config.json")
	if err != nil {
		t.Fatal(err)
	}
	routes, err := RoutesFromJSON(raw, "ctr:caddy")
	if err != nil {
		t.Fatal(err)
	}
	got := summarize(t, routes)
	compare(t, got, map[string]row{
		"ingress:caddy/shop.example.com/api/": {"shop.example.com", 443, true, "least_conn", "127.0.0.1:3001,127.0.0.1:3002"},
		"ingress:caddy/shop.example.com":      {"shop.example.com", 443, true, "single", "localhost:3000"},
		"ingress:caddy/grafana.example.com":   {"grafana.example.com", 443, true, "single", "grafana:3000"},
		"ingress:caddy/:8080/metrics":         {"", 8080, false, "single", "127.0.0.1:9100"},
	})
	if _, err := RoutesFromJSON([]byte("{"), ""); err == nil {
		t.Fatal("bad json")
	}
}

func TestParseAddress(t *testing.T) {
	for in, want := range map[string]address{
		"example.com":              {"example.com", 443, true},
		"http://example.com":       {"example.com", 80, false},
		"https://example.com:8443": {"example.com", 8443, true},
		":8080":                    {"", 8080, false},
		"localhost":                {"localhost", 80, false},
		"*.example.com":            {"*.example.com", 443, true},
		"example.com:80":           {"example.com", 80, false},
		"127.0.0.1:443":            {"127.0.0.1", 443, true},
	} {
		if got := parseAddress(in); got != want {
			t.Errorf("%s: %+v want %+v", in, got, want)
		}
	}
}
