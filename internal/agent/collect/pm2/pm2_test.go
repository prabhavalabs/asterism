package pm2

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/collecttest"
	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
)

func TestAppName(t *testing.T) {
	for in, want := range map[string]string{"shop-api-0.pid": "shop-api", "worker-12.pid": "worker", "plain.pid": "plain", "a-b-c.pid": "a-b-c"} {
		if got := AppName(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestPM2(t *testing.T) {
	root := collecttest.CopyTree(t, "../../procfs/testdata/root")
	reg := targets.New()
	s := New(config.PM2{Enabled: config.TriAuto}, 30*time.Second, reg)
	if _, err := s.Init(context.Background(), state.Env{Root: root}); err != nil {
		t.Fatal(err)
	}
	if h := s.Homes(); len(h) != 1 || h[0] != "/home/deploy/.pm2" {
		t.Fatalf("homes %v", h)
	}
	sink := collecttest.New()
	if err := s.Tick(time.Unix(1_800_000_000, 0), sink); err != nil {
		t.Fatal(err)
	}
	api := sink.Workloads["proc:shop-api"]
	if api == nil || api.Pid != 4242 || api.Replicas != 1 || api.State != "online" || api.Labels["asterism.source"] != "pm2" {
		t.Fatalf("shop-api %v", api)
	}
	if w := sink.Workloads["proc:billing-worker"]; w == nil || w.Pid != 777 {
		t.Fatalf("billing-worker %v", w)
	}
	if w := sink.Workloads["proc:old"]; w == nil || w.State != "stopped" {
		t.Fatalf("old %v", w)
	}
	for _, w := range sink.Workloads {
		if strings.Contains(w.String(), "s3cr3t") {
			t.Fatal("dump.pm2 content leaked")
		}
	}
	if reg.ProcOwner(4242) != "proc:shop-api" {
		t.Fatal("registry")
	}
	// pm2 delete removes the pid file -> the workload goes away.
	if err := os.Remove(filepath.Join(root, "home/deploy/.pm2/pids/old-3.pid")); err != nil {
		t.Fatal(err)
	}
	_ = s.Tick(time.Unix(1_800_000_030, 0), sink)
	if _, ok := sink.Workloads["proc:old"]; ok {
		t.Fatal("deleted app still present")
	}
}

func TestNoHomes(t *testing.T) {
	if _, err := New(config.PM2{}, time.Second, nil).Init(context.Background(), state.Env{Root: t.TempDir()}); err == nil {
		t.Fatal("no homes")
	}
}
