package listeners

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/collecttest"
	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

const (
	idA = "317e4d06ef07a1453738080b7345f935f359dd2c0521ffffcf9bb62ab05fed25"
	idB = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
)

func setup(t *testing.T, resolve bool) (*Source, *targets.Registry, string) {
	t.Helper()
	root := collecttest.CopyTree(t, "../../procfs/testdata/root")
	reg := targets.New()
	reg.SetContainer(targets.Container{EntityID: "ctr:shop-api-1", Name: "shop-api-1", FullID: idA, Pid: 4242, Running: true, OwnNetns: true, HostPorts: map[uint32]uint32{18080: 8080}})
	reg.SetContainer(targets.Container{EntityID: "ctr:cache-redis", Name: "cache-redis", FullID: idB, Pid: 4343, Running: true})
	reg.SetProcs("proc:billing", []int{777})
	cfg := config.Default().Sources.Ports
	cfg.ResolveOwners = resolve
	s := New(cfg, 15*time.Second, reg)
	caps, err := s.Init(context.Background(), state.Env{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if resolve && (len(caps) != 2 || caps[1] != "port_owners") {
		t.Fatalf("caps %v", caps)
	}
	return s, reg, root
}

type row struct {
	workload, name, proc string
	cport                uint32
	exp                  v1.PortExposure
}

func TestListenersWithOwners(t *testing.T) {
	s, reg, _ := setup(t, true)
	sink := collecttest.New()
	if err := s.Tick(time.Unix(1_800_000_000, 0), sink); err != nil {
		t.Fatal(err)
	}
	got := map[string]row{}
	for _, p := range sink.Listeners {
		got[p.Id] = row{p.WorkloadId, p.WorkloadName, p.ProcessName, p.ContainerPort, p.Exposure}
	}
	pub, lo := v1.PortExposure_PORT_EXPOSURE_PUBLIC, v1.PortExposure_PORT_EXPOSURE_LOOPBACK
	want := map[string]row{
		"port:tcp:127.0.0.1:8080":   {"proc:billing", "billing", "my (weird) app", 8080, lo},
		"port:tcp:0.0.0.0:22":       {"host", "", "", 22, pub},
		"port:tcp:0.0.0.0:18080":    {"ctr:shop-api-1", "shop-api-1", "docker-proxy", 8080, pub},
		"port:tcp:192.168.100.1:80": {"unit:nginx.service", "nginx.service", "nginx", 80, pub},
		"port:tcp:[::]:80":          {"unit:nginx.service", "nginx.service", "nginx", 80, pub},
		"port:tcp:127.0.0.1:5432":   {"ctr:cache-redis", "cache-redis", "redis-server", 5432, lo},
		"port:tcp:[::1]:6379":       {"host", "", "", 6379, lo},
		"port:udp:127.0.0.53:53":    {"host", "", "", 53, lo},
	}
	for id, w := range want {
		if g, ok := got[id]; !ok || g != w {
			t.Errorf("%s:\n got %+v (%v)\nwant %+v", id, g, ok, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d listeners: %v", len(got), got)
	}
	if !reg.IsListening(888) || reg.IsListening(4242) {
		t.Fatal("listening pids")
	}

	// Steady state: all inodes are known or recently unresolved -> no walk.
	s.procDir = "/nonexistent" // a walk would now fail to find anything
	sink2 := collecttest.New()
	_ = s.Tick(time.Unix(1_800_000_015, 0), sink2)
	if len(sink2.Listeners) != len(want) {
		t.Fatal("scan changed")
	}
	for _, p := range sink2.Listeners {
		if p.Id == "port:tcp:127.0.0.1:8080" && p.WorkloadId != "proc:billing" {
			t.Fatal("cached owner lost")
		}
	}
}

func TestListenersWithoutOwners(t *testing.T) {
	s, _, root := setup(t, false)
	_ = root
	sink := collecttest.New()
	if err := s.Tick(time.Unix(1_800_000_000, 0), sink); err != nil {
		t.Fatal(err)
	}
	for _, p := range sink.Listeners {
		switch p.Id {
		case "port:tcp:0.0.0.0:18080":
			if p.WorkloadId != "ctr:shop-api-1" || p.ContainerPort != 8080 {
				t.Fatalf("published port not attributed without owners: %v", p)
			}
		case "port:tcp:127.0.0.1:8080":
			if p.WorkloadId != "host" || p.ProcessName != "" {
				t.Fatalf("owner resolved without capability: %v", p)
			}
		}
	}
}

func TestResolveProbeDegrades(t *testing.T) {
	root := collecttest.CopyTree(t, "../../procfs/testdata/root")
	if err := os.RemoveAll(filepath.Join(root, "proc/1/fd")); err != nil {
		t.Fatal(err)
	}
	s := New(config.Default().Sources.Ports, time.Second, nil)
	caps, err := s.Init(context.Background(), state.Env{Root: root})
	if err != nil || len(caps) != 1 || s.resolve {
		t.Fatalf("caps %v err %v resolve %v", caps, err, s.resolve)
	}
	if _, err := New(config.Default().Sources.Ports, time.Second, nil).Init(context.Background(), state.Env{Root: t.TempDir()}); err == nil {
		t.Fatal("init without socket tables")
	}
}
