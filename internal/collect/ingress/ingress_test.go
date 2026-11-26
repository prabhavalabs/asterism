package ingress

import (
	"testing"

	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func TestUpstreamAddress(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:3000/api": "127.0.0.1:3000",
		"https://backend":           "backend:443",
		"http://backend":            "backend:80",
		"http://$upstream":          "$upstream",
		"localhost:8080":            "localhost:8080",
		"localhost:8080/x":          "localhost:8080",
		"unix:/run/app.sock":        "unix:/run/app.sock",
		"unix:/run/app.sock:":       "unix:/run/app.sock",
		"grpc://[::1]:50051":        "[::1]:50051",
		"":                          "",
	} {
		if got := UpstreamAddress(in); got != want {
			t.Errorf("%q: %q want %q", in, got, want)
		}
	}
}

func TestFinishAndHelpers(t *testing.T) {
	rs := Finish([]*v1.IngressRoute{
		{Id: "b", Hostnames: []string{"b.test"}}, {Id: "a", Hostnames: []string{"a.test"}}, {Id: "b"}, nil,
	})
	if len(rs) != 2 || rs[0].Id != "a" {
		t.Fatalf("%v", rs)
	}
	if RouteID("nginx", "x.test", "/") != "ingress:nginx/x.test" || RouteID("nginx", "x.test", "/api") != "ingress:nginx/x.test/api" {
		t.Fatal("RouteID")
	}
	if Strategy("", 1) != "single" || Strategy("", 2) != "round_robin" || Strategy("ip_hash", 3) != "ip_hash" {
		t.Fatal("Strategy")
	}
	h := AddHost(nil, "A.Test")
	h = AddHost(h, "a.test")
	h = AddHost(h, "_")
	h = AddHost(h, "~^re$")
	if len(h) != 1 || h[0] != "a.test" {
		t.Fatalf("%v", h)
	}
	m := MapReader{"/etc/x/a.conf": "1", "/etc/x/b.conf": "2", "/etc/y.conf": "3"}
	g, _ := m.Glob("/etc/x/*.conf")
	if len(g) != 2 {
		t.Fatal(g)
	}
	if _, err := m.ReadFile("/nope"); err == nil {
		t.Fatal("missing file")
	}
}
