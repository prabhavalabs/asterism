package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/agent/collect/collecttest"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

const ingressData = "../../../collect/ingress"

// hostRoot builds a host root with nginx (sites-enabled entry as an
// absolute symlink, as Debian creates them), haproxy and cloudflared.
func hostRoot(t *testing.T) string {
	t.Helper()
	root := collecttest.CopyTree(t, ingressData+"/nginx/testdata")
	en := filepath.Join(root, "etc/nginx/sites-enabled")
	av := filepath.Join(root, "etc/nginx/sites-available")
	if err := os.MkdirAll(av, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(en, "shop"), filepath.Join(av, "shop")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/nginx/sites-available/shop", filepath.Join(en, "shop")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"etc/haproxy", "etc/cloudflared", "sys/fs/cgroup/system.slice/nginx.service"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cp := func(src, dst string) {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dst), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cp(ingressData+"/haproxy/testdata/haproxy.cfg", "etc/haproxy/haproxy.cfg")
	cp(ingressData+"/cloudflared/testdata/config.yml", "etc/cloudflared/config.yml")
	return root
}

func TestProxySource(t *testing.T) {
	root := hostRoot(t)
	cfg := config.Default().Sources
	cfg.Proxies.Caddy.AdminAPI = "" // no admin API in this test
	cfg.Routes = []config.Route{{Proxy: "cloudflared", Hostnames: []string{"tunnel.example.com"}, Upstream: "http://127.0.0.1:3001"}}
	s := New(cfg.Proxies, cfg.Routes, 30*time.Second)
	caps, err := s.Init(context.Background(), state.Env{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(caps, ",") != "nginx,haproxy,cloudflared" {
		t.Fatalf("caps %v", caps)
	}
	sink := collecttest.New()
	if err := s.Tick(time.Unix(1, 0), sink); err != nil {
		t.Fatal(err)
	}
	byID := map[string]*v1.IngressRoute{}
	for _, r := range sink.Routes {
		byID[r.Id] = r
		b, _ := proto.Marshal(r)
		if strings.Contains(string(b), "s3cr3t") {
			t.Fatalf("%s leaks a secret", r.Id)
		}
	}
	for _, id := range []string{
		"ingress:nginx/shop.example.com", // via the absolute symlink inside the root
		"ingress:nginx/shop.example.com/api/",
		"ingress:haproxy/shop.example.com",
		"ingress:cloudflared/status.example.com",
		"ingress:cloudflared/tunnel.example.com", // manual route
	} {
		if byID[id] == nil {
			t.Errorf("missing %s", id)
		}
	}
	if r := byID["ingress:nginx/shop.example.com"]; r != nil && r.ProxyWorkloadId != "unit:nginx.service" {
		t.Errorf("proxy workload %q", r.ProxyWorkloadId)
	}
	if r := byID["ingress:haproxy/shop.example.com"]; r != nil && r.ProxyWorkloadId != "" {
		t.Errorf("haproxy unit does not exist: %q", r.ProxyWorkloadId)
	}

	// Unchanged files: no re-parse.
	before := s.files[0].routes
	_ = s.Tick(time.Unix(31, 0), sink)
	if &before[0] != &s.files[0].routes[0] {
		t.Fatal("nginx re-parsed without a change")
	}
	// A new file in an include directory triggers a re-parse.
	if err := os.WriteFile(filepath.Join(root, "etc/nginx/conf.d/new.conf"), []byte("server { listen 80; server_name new.example.com; location / { proxy_pass http://127.0.0.1:7000; } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Directory mtime granularity can be coarse; also bump the dir mtime.
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(root, "etc/nginx/conf.d"), future, future)
	_ = s.Tick(time.Unix(61, 0), sink)
	found := false
	for _, r := range sink.Routes {
		found = found || r.Id == "ingress:nginx/new.example.com"
	}
	if !found {
		t.Fatal("new include file not picked up")
	}
}

func TestHostFSResolve(t *testing.T) {
	root := hostRoot(t)
	h := HostFS{Root: root}
	real, err := h.Resolve("/etc/nginx/sites-enabled/shop")
	if err != nil || real != filepath.Join(root, "etc/nginx/sites-available/shop") {
		t.Fatalf("%q %v", real, err)
	}
	if err := os.Symlink("../../../../../../etc/passwd", filepath.Join(root, "etc/nginx/escape")); err != nil {
		t.Fatal(err)
	}
	if r, err := h.Resolve("/etc/nginx/escape"); err == nil && !strings.HasPrefix(r, root) {
		t.Fatalf("relative symlink escaped the root: %s", r)
	}
	if err := os.Symlink("loop", filepath.Join(root, "etc/loop")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ReadFile("/etc/loop"); err == nil {
		t.Fatal("symlink loop")
	}
	g, err := h.Glob("/etc/nginx/sites-enabled/*")
	if err != nil || len(g) != 2 {
		t.Fatalf("glob %v %v", g, err)
	}
}

func TestCaddyAdminAPI(t *testing.T) {
	cfgJSON, err := os.ReadFile(ingressData + "/caddy/testdata/admin-config.json")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		_, _ = w.Write(cfgJSON)
	}))
	defer srv.Close()
	pc := config.Default().Sources.Proxies
	pc.Caddy.AdminAPI = srv.URL
	for _, p := range []*config.Tri{&pc.Nginx.Enabled, &pc.HAProxy.Enabled, &pc.Cloudflared.Enabled} {
		*p = config.TriFalse
	}
	s := New(pc, nil, time.Second)
	caps, err := s.Init(context.Background(), state.Env{Root: t.TempDir()})
	if err != nil || strings.Join(caps, ",") != "caddy_api" {
		t.Fatalf("caps %v %v", caps, err)
	}
	s.PollAPIs(context.Background())
	sink := collecttest.New()
	_ = s.Tick(time.Unix(1, 0), sink)
	// One probe at Init (enabled: auto) plus one poll; only GET /config/.
	if len(sink.Routes) != 4 || len(paths) != 2 || paths[0] != "GET /config/" || paths[1] != "GET /config/" {
		t.Fatalf("routes %d paths %v", len(sink.Routes), paths)
	}
}

func TestNothingConfigured(t *testing.T) {
	pc := config.Default().Sources.Proxies
	pc.Caddy.AdminAPI = "http://127.0.0.1:1" // auto + not answering = not detected
	if _, err := New(pc, nil, time.Second).Init(context.Background(), state.Env{Root: t.TempDir()}); err == nil {
		t.Fatal("no proxies: init must fail so the source is disabled")
	}
}
