package process

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/collecttest"
	"github.com/prabhavalabs/asterism/internal/agent/collect/redact"
	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

func TestWildcard(t *testing.T) {
	cases := []struct {
		p, s string
		want bool
	}{
		{"*billing/server.js*", "/usr/bin/node /srv/billing/server.js --port 1", true},
		{"/usr/bin/node", "/usr/bin/node", true},
		{"/usr/bin/n?de", "/usr/bin/node", true},
		{"*.js", "a/b.js", true},
		{"*.js", "a/b.jsx", false},
		{"", "", true},
		{"*", "", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
	}
	for _, c := range cases {
		if got := wildcard(c.p, c.s); got != c.want {
			t.Errorf("wildcard(%q,%q)=%v", c.p, c.s, got)
		}
	}
}

func TestProcessRules(t *testing.T) {
	root := collecttest.CopyTree(t, "../../procfs/testdata/root")
	rules := []config.ProcessMatch{
		{Name: "billing", Exe: "/usr/bin/node", Cmdline: "*billing/server.js*", User: "deploy", Role: "app"},
		{Name: "nginx"}, // comm match
		{Name: "containerized", Cmdline: "node server.js*"}, // pid 4242 is in a container: never matched
		{Name: "absent", Cmdline: "*nothing-runs-this*"},
	}
	red, _ := redact.New(config.Redaction{})
	reg := targets.New()
	s := New(rules, 30*time.Second, reg, red)
	if _, err := s.Init(context.Background(), state.Env{Root: root}); err != nil {
		t.Fatal(err)
	}
	sink := collecttest.New()
	t0 := time.Unix(1_800_000_000, 0)
	if err := s.Tick(t0, sink); err != nil {
		t.Fatal(err)
	}
	b := sink.Workloads["proc:billing"]
	if b == nil || b.Pid != 777 || b.Kind != v1.WorkloadKind_WORKLOAD_KIND_PROCESS || b.Role != v1.WorkloadRole_WORKLOAD_ROLE_APP || b.State != "running" || b.Replicas != 1 {
		t.Fatalf("billing %v", b)
	}
	if c := b.Labels["process.cmdline"]; c != "/usr/bin/node /srv/billing/server.js --token [REDACTED]" || strings.Contains(b.String(), "s3cr3t") {
		t.Fatalf("cmdline %q", c)
	}
	// starttime 6000000 ticks = 60000 s after btime.
	if b.Labels["process.user"] != "deploy" || b.StartedAtMs != 1795500000000+60000000 {
		t.Fatalf("user %q started %d", b.Labels["process.user"], b.StartedAtMs)
	}
	if n := sink.Workloads["proc:nginx"]; n == nil || n.Pid != 888 {
		t.Fatalf("nginx %v", n)
	}
	if c := sink.Workloads["proc:containerized"]; c == nil || c.State != "stopped" || c.Health != v1.HealthStatus_HEALTH_STATUS_DOWN {
		t.Fatalf("container process matched: %v", c)
	}
	if reg.ProcOwner(777) != "proc:billing" {
		t.Fatal("registry")
	}
	if v, ok := sink.Value("proc:billing", "mem.used_bytes"); !ok || v != float32(5120*os.Getpagesize()) {
		t.Fatalf("rss %v", v)
	}

	// +300 ticks (3 s CPU) in 30 s -> 0.1 cores.
	st := filepath.Join(root, "proc/777/stat")
	b0, _ := os.ReadFile(st)
	if err := os.WriteFile(st, []byte(strings.Replace(string(b0), " 4200 1300 ", " 4400 1400 ", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	sink.Reset()
	_ = s.Tick(t0.Add(30*time.Second), sink)
	if v, ok := sink.Value("proc:billing", "cpu.util"); !ok || v < 0.0999 || v > 0.1001 {
		t.Fatalf("cpu %v %v", v, ok)
	}
	// pid reuse: a new process with the same pid and a different start time
	// no longer matches.
	if err := os.WriteFile(st, []byte(strings.Replace(string(b0), " 6000000 ", " 7000000 ", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proc/777/cmdline"), []byte("/bin/other\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = s.Tick(t0.Add(60*time.Second), sink)
	if w := sink.Workloads["proc:billing"]; w.State != "stopped" {
		t.Fatalf("pid reuse not detected: %v", w)
	}
}

func TestInitNeedsRules(t *testing.T) {
	if _, err := New(nil, time.Second, nil, nil).Init(context.Background(), state.Env{}); err == nil {
		t.Fatal("no rules")
	}
	if _, err := New([]config.ProcessMatch{{Cmdline: "x"}}, time.Second, nil, nil).Init(context.Background(), state.Env{}); err == nil {
		t.Fatal("rule without name")
	}
}
