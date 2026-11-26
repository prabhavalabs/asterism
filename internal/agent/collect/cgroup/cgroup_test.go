package cgroup

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/collecttest"
	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/state"
)

const (
	idA = "317e4d06ef07a1453738080b7345f935f359dd2c0521ffffcf9bb62ab05fed25"
	idB = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
)

func write(t testing.TB, root, p, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, p), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func approx(a float32, b float64) bool {
	return math.Abs(float64(a)-b) <= math.Max(1e-3, math.Abs(b)*1e-4)
}

func TestSystemdLayout(t *testing.T) {
	root := collecttest.CopyTree(t, "../../procfs/testdata/root")
	reg := targets.New()
	reg.SetContainer(targets.Container{EntityID: "ctr:shop-api-1", Name: "shop-api-1", FullID: idA, Pid: 4242, OwnNetns: true, Running: true})
	reg.SetContainer(targets.Container{EntityID: "ctr:stopped", Name: "stopped", FullID: idB, Running: false})
	reg.SetUnits([]targets.Unit{{Name: "nginx.service", Dir: filepath.Join(root, "sys/fs/cgroup/system.slice/nginx.service")}})
	s := New(reg, 5*time.Second)
	caps, err := s.Init(context.Background(), state.Env{Root: root})
	if err != nil || len(caps) != 1 || caps[0] != "cgroup_v2" {
		t.Fatalf("init: %v %v", caps, err)
	}
	sink := collecttest.New()
	t0 := time.Unix(1_800_000_000, 0)
	if err := s.Tick(t0, sink); err != nil {
		t.Fatal(err)
	}
	if s.Targets() != 2 {
		t.Fatalf("targets %d", s.Targets())
	}
	// First tick: gauges only.
	if v, ok := sink.Value("ctr:shop-api-1", "mem.used_bytes"); !ok || v != 104857600-20971520 {
		t.Fatalf("mem %v %v", v, ok)
	}
	if v, _ := sink.Value("ctr:shop-api-1", "pids.count"); v != 11 {
		t.Fatalf("pids %v", v)
	}
	if _, ok := sink.Value("ctr:shop-api-1", "cpu.util"); ok {
		t.Fatal("rate on first sample")
	}
	cg := "sys/fs/cgroup/system.slice/docker-" + idA + ".scope/"
	write(t, root, cg+"cpu.stat", "usage_usec 4000000\n")                                 // +2.5 s CPU in 5 s
	write(t, root, cg+"io.stat", "8:0 rbytes=6291456 wbytes=2097152\n")                   // +4 MiB read
	write(t, root, "proc/4242/net/dev", strings.Replace(netdev, "2000000", "2500000", 1)) // +500000 rx
	write(t, root, "sys/fs/cgroup/system.slice/nginx.service/memory.events", "oom_kill 2\n")
	sink.Reset()
	if err := s.Tick(t0.Add(5*time.Second), sink); err != nil {
		t.Fatal(err)
	}
	checks := map[string]float64{
		"ctr:shop-api-1|cpu.util":      0.5,
		"ctr:shop-api-1|blk.read_bps":  4194304.0 / 5,
		"ctr:shop-api-1|blk.write_bps": 0,
		"ctr:shop-api-1|net.rx_bps":    100000,
		"ctr:shop-api-1|net.tx_bps":    0,
		"ctr:shop-api-1|oom.count":     0,
		"unit:nginx.service|cpu.util":  0,
		"unit:nginx.service|oom.count": 2,
	}
	for k, want := range checks {
		got, ok := sink.Values[k]
		if !ok || !approx(got, want) {
			t.Errorf("%s = %v (%v) want %v", k, got, ok, want)
		}
	}
	if _, ok := sink.Value("unit:nginx.service", "net.rx_bps"); ok {
		t.Error("units share the host netns: no net series")
	}
	if len(sink.Events) != 1 || sink.Events[0].Kind != "unit.oom" || sink.Events[0].EntityId != "unit:nginx.service" {
		t.Fatalf("events %v", sink.Events)
	}

	// The container goes away: the target is dropped on the next rebuild.
	reg.RemoveContainer(idA)
	sink.Reset()
	_ = s.Tick(t0.Add(10*time.Second), sink)
	if s.Targets() != 1 {
		t.Fatalf("targets after removal %d", s.Targets())
	}
	s.Close()
}

const netdev = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo:    1000      10    0    0    0     0          0         0     1000      10    0    0    0     0       0          0
  eth0: 2000000   1500    0    0    0     0          0         0   300000    1200    0    0    0     0       0          0
`

func TestCgroupfsLayoutPrivateNamespace(t *testing.T) {
	root := collecttest.CopyTree(t, "../../procfs/testdata/root-cgroupfs")
	reg := targets.New()
	// /proc/4343/cgroup reads "0::/../../docker/<id>" (private cgroupns).
	reg.SetContainer(targets.Container{EntityID: "ctr:cache-redis", Name: "cache-redis", FullID: idB, Pid: 4343, OwnNetns: true, Running: true})
	s := New(reg, 5*time.Second)
	if _, err := s.Init(context.Background(), state.Env{Root: root}); err != nil {
		t.Fatal(err)
	}
	sink := collecttest.New()
	t0 := time.Unix(1_800_000_000, 0)
	_ = s.Tick(t0, sink)
	if s.Targets() != 1 {
		t.Fatalf("cgroupfs container not resolved")
	}
	// 32-bit wrap of eth0 rx between samples.
	write(t, root, "proc/4343/net/dev", strings.Replace(mustRead(t, root, "proc/4343/net/dev"), "4294967000", "200", 1))
	sink.Reset()
	_ = s.Tick(t0.Add(4*time.Second), sink)
	if v, ok := sink.Value("ctr:cache-redis", "net.rx_bps"); !ok || !approx(v, 496.0/4) {
		t.Fatalf("wrapped rx rate %v %v", v, ok)
	}
	if v, _ := sink.Value("ctr:cache-redis", "oom.count"); v != 1 {
		t.Fatalf("oom %v", v)
	}
}

func mustRead(t testing.TB, root, p string) string {
	b, err := os.ReadFile(filepath.Join(root, p))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInitWithoutCgroupV2(t *testing.T) {
	s := New(targets.New(), time.Second)
	if _, err := s.Init(context.Background(), state.Env{Root: t.TempDir()}); err == nil {
		t.Fatal("init must fail without cgroup v2")
	}
}

// setup50 builds a host root with 50 running containers (systemd layout).
func setup50(b *testing.B) (*Source, *targets.Registry, string) {
	root := b.TempDir()
	src := "../../procfs/testdata/root/sys/fs/cgroup/system.slice/docker-" + idA + ".scope"
	if err := os.MkdirAll(filepath.Join(root, "sys/fs/cgroup"), 0o755); err != nil {
		b.Fatal(err)
	}
	write(b, root, "sys/fs/cgroup/cgroup.controllers", "cpu io memory pids\n")
	reg := targets.New()
	for i := range 50 {
		id := fmt.Sprintf("%064x", i+1)
		pid := 10000 + i
		dir := filepath.Join(root, "sys/fs/cgroup/system.slice", "docker-"+id+".scope")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		for _, f := range []string{"cgroup.procs", "cpu.stat", "memory.current", "memory.stat", "io.stat", "pids.current", "memory.events"} {
			c, err := os.ReadFile(filepath.Join(src, f))
			if err != nil {
				b.Fatal(err)
			}
			write(b, dir, f, string(c))
		}
		pdir := filepath.Join(root, "proc", fmt.Sprint(pid), "net")
		if err := os.MkdirAll(pdir, 0o755); err != nil {
			b.Fatal(err)
		}
		write(b, pdir, "dev", netdev)
		write(b, filepath.Join(root, "proc", fmt.Sprint(pid)), "cgroup", "0::/system.slice/docker-"+id+".scope\n")
		reg.SetContainer(targets.Container{EntityID: fmt.Sprintf("ctr:app-%02d", i), Name: fmt.Sprintf("app-%02d", i), FullID: id, Pid: int32(pid), OwnNetns: true, Running: true})
	}
	s := New(reg, 5*time.Second)
	if _, err := s.Init(context.Background(), state.Env{Root: root}); err != nil {
		b.Fatal(err)
	}
	return s, reg, root
}

// BenchmarkTick50 is the steady-state 50-container cgroup tick through a
// real state.Store handle (Tick + Commit). Budget: ≤ 5 allocs/op.
func BenchmarkTick50(b *testing.B) {
	s, _, _ := setup50(b)
	store := state.NewStore(state.Options{})
	h := store.Sink("cgroup")
	now := time.Unix(1_800_000_000, 0)
	for range 3 { // warm up: open files, intern series, grow buffers
		now = now.Add(5 * time.Second)
		_ = s.Tick(now, h)
		h.Commit(now, true)
	}
	_ = store.TakeMetrics()
	if s.Targets() != 50 {
		b.Fatalf("targets %d", s.Targets())
	}
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		now = now.Add(5 * time.Second)
		_ = s.Tick(now, h)
		h.Commit(now, true)
		if i++; i%1024 == 0 {
			b.StopTimer()
			_ = store.TakeMetrics()
			b.StartTimer()
		}
	}
	b.StopTimer()
	s.Close()
}

// BenchmarkTick50Only measures the collector without the store commit.
func BenchmarkTick50Only(b *testing.B) {
	s, _, _ := setup50(b)
	sink := &discardSink{}
	now := time.Unix(1_800_000_000, 0)
	for range 3 {
		now = now.Add(5 * time.Second)
		_ = s.Tick(now, sink)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		now = now.Add(5 * time.Second)
		_ = s.Tick(now, sink)
	}
	b.StopTimer()
	s.Close()
}

type discardSink struct {
	collecttest.Sink
	n state.SeriesID
}

func (d *discardSink) Series(string, string) state.SeriesID { d.n++; return d.n }
func (d *discardSink) Set(state.SeriesID, float32)          {}
