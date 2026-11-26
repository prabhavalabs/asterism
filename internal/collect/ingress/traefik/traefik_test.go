package traefik

import (
	"os"
	"strings"
	"testing"
)

func TestParseRule(t *testing.T) {
	cases := map[string][2]string{
		"Host(`a.example.com`)": {"a.example.com", "/"},
		"Host(`a.example.com`, `b.example.com`) && PathPrefix(`/api`)": {"a.example.com,b.example.com", "/api"},
		"Host(\"a.example.com\") || Host(\"c.example.com\")":           {"a.example.com,c.example.com", "/"},
		"HostSNI(`db.example.com`)":                                    {"db.example.com", "/"},
		"PathPrefix(`/x`)":                                             {"", "/x"},
	}
	for rule, want := range cases {
		h, p := ParseRule(rule)
		if strings.Join(h, ",") != want[0] || p != want[1] {
			t.Errorf("%s: %v %q", rule, h, p)
		}
	}
}

func TestParseLabels(t *testing.T) {
	labels := map[string]string{
		"traefik.enable":                                       "true",
		"traefik.http.routers.shop.rule":                       "Host(`shop.example.com`)",
		"traefik.http.routers.shop.entrypoints":                "websecure",
		"traefik.http.routers.shop.tls.certresolver":           "le",
		"traefik.http.routers.shop-admin.rule":                 "Host(`shop.example.com`) && PathPrefix(`/admin`)",
		"traefik.http.routers.shop-admin.service":              "admin",
		"traefik.http.services.web.loadbalancer.server.port":   "3000",
		"traefik.http.services.admin.loadbalancer.server.port": "3100",
		"traefik.http.middlewares.auth.basicauth.users":        "admin:$apr1$s3cr3t-value",
	}
	routes := ParseLabels("shop-web-1", labels, 0)
	got := map[string]string{}
	for _, r := range routes {
		got[r.Id] = r.Upstreams[0].Address + " " + strings.Join(r.Hostnames, ",")
		if strings.Contains(r.String(), "s3cr3t") {
			t.Fatal("leak")
		}
	}
	want := map[string]string{
		"ingress:traefik/shop.example.com":       "shop-web-1:3000 shop.example.com",
		"ingress:traefik/shop.example.com/admin": "shop-web-1:3100 shop.example.com",
	}
	if len(got) != 2 || got["ingress:traefik/shop.example.com"] != want["ingress:traefik/shop.example.com"] || got["ingress:traefik/shop.example.com/admin"] != want["ingress:traefik/shop.example.com/admin"] {
		t.Fatalf("%v", got)
	}
	for _, r := range routes {
		if r.Id == "ingress:traefik/shop.example.com" && (!r.Tls || r.ListenPort != 443) {
			t.Fatalf("tls %v port %d", r.Tls, r.ListenPort)
		}
	}
	if ParseLabels("x", map[string]string{"traefik.enable": "false", "traefik.http.routers.a.rule": "Host(`a.b`)"}, 80) != nil {
		t.Fatal("traefik.enable=false")
	}
	r := ParseLabels("api", map[string]string{"traefik.http.routers.a.rule": "Host(`a.example.com`)"}, 8080)
	if len(r) != 1 || r[0].Upstreams[0].Address != "api:8080" || r[0].ListenPort != 80 {
		t.Fatalf("%v", r)
	}
}

func TestRoutesFromAPI(t *testing.T) {
	rj, _ := os.ReadFile("testdata/routers.json")
	sj, _ := os.ReadFile("testdata/services.json")
	routes, err := RoutesFromAPI(rj, sj, "ctr:traefik")
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Id != "ingress:traefik/api.example.com/v1" || len(routes[0].Upstreams) != 2 || routes[0].Upstreams[0].Address != "172.20.0.3:8080" || routes[0].Strategy != "round_robin" || !routes[0].Tls {
		t.Fatalf("%v", routes)
	}
	if _, err := RoutesFromAPI([]byte("{"), sj, ""); err == nil {
		t.Fatal("bad json")
	}
}
