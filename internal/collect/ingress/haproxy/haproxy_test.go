package haproxy

import (
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func TestParse(t *testing.T) {
	src, err := os.ReadFile("testdata/haproxy.cfg")
	if err != nil {
		t.Fatal(err)
	}
	routes := Parse(src, "unit:haproxy.service")
	type row struct {
		hosts, strategy, ups string
		port                 uint32
		tls                  bool
	}
	got := map[string]row{}
	for _, r := range routes {
		b, _ := proto.Marshal(r)
		if strings.Contains(string(b), "s3cr3t") || strings.Contains(string(b), ".pem") {
			t.Fatalf("%s leaks secrets or cert paths", r.Id)
		}
		if r.ProxyKind != v1.ProxyKind_PROXY_KIND_HAPROXY {
			t.Errorf("%s proxy kind %v, want HAPROXY", r.Id, r.ProxyKind)
		}
		var ups []string
		for _, u := range r.Upstreams {
			s := u.Address
			if u.Backup {
				s += "(backup)"
			}
			ups = append(ups, s)
		}
		got[r.Id] = row{strings.Join(r.Hostnames, ","), r.Strategy, strings.Join(ups, ","), r.ListenPort, r.Tls}
	}
	want := map[string]row{
		"ingress:haproxy/shop.example.com": {"shop.example.com,www.shop.example.com", "leastconn", "127.0.0.1:3001,127.0.0.1:3002,10.0.0.9:3001(backup)", 443, true},
		"ingress:haproxy/api.example.com":  {"api.example.com", "single", "127.0.0.1:4000", 443, true},
		"ingress:haproxy/https_in":         {"", "single", "127.0.0.1:8080", 443, true},
		"ingress:haproxy/postgres":         {"", "single", "127.0.0.1:5432", 5433, false},
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s:\n got %+v\nwant %+v", id, got[id], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("routes %v", got)
	}
	if routes[0].Upstreams[0].Weight == 0 {
		t.Error("weights")
	}
}
