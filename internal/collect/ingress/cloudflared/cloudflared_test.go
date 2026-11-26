package cloudflared

import (
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestParse(t *testing.T) {
	src, err := os.ReadFile("testdata/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	routes, err := Parse(src, "unit:cloudflared.service")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range routes {
		b, _ := proto.Marshal(r)
		if strings.Contains(string(b), "s3cr3t") || strings.Contains(string(b), ".json") {
			t.Fatalf("%s leaks", r.Id)
		}
		got[r.Id] = r.PathPrefix + " " + r.Upstreams[0].Address
	}
	want := map[string]string{
		"ingress:cloudflared/status.example.com":    "/ 127.0.0.1:3001",
		"ingress:cloudflared/shop.example.com/api/": "/api/ localhost:8080",
		"ingress:cloudflared/ssh.example.com":       "/ localhost:22",
		"ingress:cloudflared/sock.example.com":      "/ unix:/run/app.sock",
	}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q want %q", k, got[k], v)
		}
	}
	if _, err := Parse([]byte("ingress: [ {"), ""); err == nil {
		t.Fatal("bad yaml")
	}
}
