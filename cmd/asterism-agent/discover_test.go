package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prabhavalabs/asterism/internal/agent/dockerapi"
	"github.com/prabhavalabs/asterism/internal/agent/dockerapi/dockertest"
)

// TestDiscover runs the dry run against the procfs fixture host root and a
// fake Docker engine: every source reports, and no fixture secret is printed.
func TestDiscover(t *testing.T) {
	eng := dockertest.New(t, "../../internal/collect/dockercli/testdata/engine", dockerapi.Allowed)
	root, err := filepath.Abs("../../internal/agent/procfs/testdata/root")
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "agent.yaml")
	yaml := `hub: {url: "http://127.0.0.1:1", token: "asta_test"}
host: {root: "` + root + `", name: test-1}
sources:
  docker: {endpoint: "` + eng.Endpoint + `"}
  processes:
    match:
      - {name: billing, cmdline: "*billing/server.js*", role: app}
  proxies:
    caddy: {admin_api: ""}
projects:
  - {name: shop, members: {compose: [shop]}}
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := runMain([]string{"discover", "--config", cfgPath}, &out, &errb); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
	s := out.String()
	for _, want := range []string{
		"host       on", "docker     on", "listeners  on", "systemd    on", "cgroup     on", "pm2        on", "processes  on",
		"shop-api-1", "cache-redis", "unit:nginx.service", "proc:billing", "proc:shop-api",
		"ctr:shop-api-1", // docker-proxy listener attributed to the container
		"configured projects:", "- name: shop", "compose:",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("discover output lacks %q\n%s", want, s)
		}
	}
	if strings.Contains(s, "s3cr3t-value") || strings.Contains(errb.String(), "s3cr3t-value") {
		t.Fatalf("discover printed a fixture secret:\n%s", s)
	}
	if strings.Contains(s, "buildx_buildkit") {
		t.Fatal("excluded container listed")
	}
	if v := eng.Violations(); len(v) != 0 {
		t.Fatalf("violations %+v", v)
	}
}
