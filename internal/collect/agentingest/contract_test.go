package agentingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/cgroup"
	"github.com/prabhavalabs/asterism/internal/agent/collect/collecttest"
	"github.com/prabhavalabs/asterism/internal/agent/collect/docker"
	"github.com/prabhavalabs/asterism/internal/agent/collect/host"
	"github.com/prabhavalabs/asterism/internal/agent/collect/listeners"
	"github.com/prabhavalabs/asterism/internal/agent/collect/redact"
	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/dockerapi"
	"github.com/prabhavalabs/asterism/internal/agent/dockerapi/dockertest"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

const (
	contractProcRoot = "../../agent/procfs/testdata/root"
	contractEngine   = "../dockercli/testdata/engine"
	contractCgroup   = "sys/fs/cgroup/system.slice/docker-317e4d06ef07a1453738080b7345f935f359dd2c0521ffffcf9bb62ab05fed25.scope/"
)

// TestAgentHubContract runs the real agent collectors (host, docker,
// cgroup, listeners) against procfs, cgroup and Docker Engine fixtures,
// encodes their output with the agent's state.Store exactly as the uplink
// does, feeds the frames through a hub session (decoder, inventory,
// assemble) and asserts that every HostInfo and Container field the UI
// reads (host vitals page, overview KPIs, container list and charts) is
// populated. It is the contract between the agent's metric names and the
// names the hub folds (view.go), which are the SSH bridge's.
func TestAgentHubContract(t *testing.T) {
	ctx := context.Background()
	root := collecttest.CopyTree(t, contractProcRoot)
	eng := dockertest.New(t, contractEngine, dockerapi.Allowed)
	cl, err := dockerapi.New(eng.Endpoint, dockerapi.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Host.Name = "prod-1"
	cfg.Host.Root = root
	red, err := redact.New(cfg.Redaction)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().Add(-time.Minute).Truncate(time.Second)
	env := state.Env{Config: cfg, Root: root, Now: func() time.Time { return t0 }}
	reg := targets.New()
	store := state.NewStore(state.Options{})

	hostSrc := host.New(cfg, 2*time.Second)
	dockerSrc := docker.New(docker.Options{Name: "docker", Docker: cfg.Sources.Docker, Interval: cfg.Intervals, Logs: cfg.Sources.Logs, Client: cl, Registry: reg, Redactor: red})
	cgSrc := cgroup.New(reg, 5*time.Second)
	lsSrc := listeners.New(cfg.Sources.Ports, 15*time.Second, reg)
	for _, s := range []state.Source{hostSrc, dockerSrc, cgSrc, lsSrc} {
		if _, err := s.Init(ctx, env); err != nil {
			t.Fatalf("init %s: %v", s.Name(), err)
		}
	}
	if err := dockerSrc.SyncOnce(ctx, store.Sink("docker.run"), true); err != nil {
		t.Fatalf("docker sync: %v", err)
	}
	ticked := []state.Source{hostSrc, cgSrc, lsSrc}
	handles := map[string]*state.Handle{}
	for _, s := range ticked {
		handles[s.Name()] = store.Sink(s.Name())
	}
	tick := func(now time.Time) {
		t.Helper()
		for _, s := range ticked {
			h := handles[s.Name()]
			err := s.Tick(now, h)
			h.Commit(now, err == nil)
			if err != nil {
				t.Fatalf("tick %s: %v", s.Name(), err)
			}
		}
	}
	tick(t0)

	// Advance every counter so each rate series has a value: host CPU
	// (+busy, +steal), eth0 rx/tx, sda read/write, and the shop-api-1
	// container's cgroup CPU, block I/O and own-netns traffic.
	write := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	replace := func(p string, pairs ...string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for i := 0; i < len(pairs); i += 2 {
			if !strings.Contains(s, pairs[i]) {
				t.Fatalf("%s: %q not in fixture", p, pairs[i])
			}
			s = strings.Replace(s, pairs[i], pairs[i+1], 1)
		}
		write(p, s)
	}
	write("proc/stat", "cpu  10132503 290696 3084719 46829083 16683 0 25195 1050 0 0\nbtime 1795500000\n")
	replace("proc/1/net/dev", "4000000000", "4002000000", "1000000000", "1000500000")
	replace("proc/diskstats", "sda 15000 2000 1200000 9000 30000 4000 2400000", "sda 15000 2000 1202000 9000 30000 4000 2401000")
	write(contractCgroup+"cpu.stat", "usage_usec 4000000\n")
	write(contractCgroup+"io.stat", "8:0 rbytes=5242880 wbytes=3145728\n259:0 rbytes=1048576 wbytes=0\n")
	replace("proc/4242/net/dev", "2000000", "2500000", "300000", "400000")
	tick(t0.Add(5 * time.Second))

	// Encode as the uplink does on a fresh connection.
	snap, _ := store.TakeInventory(0, true)
	metrics := store.TakeMetrics()
	if snap == nil || metrics == nil {
		t.Fatalf("store produced no snapshot (%v) or metrics (%v)", snap != nil, metrics != nil)
	}

	h := newTestHub(t, nil)
	s := h.mustDial("prod-1", "contract")
	drain := h.collectBus()
	s.send(&asterismv1.AgentFrame{Payload: &asterismv1.AgentFrame_Snapshot{Snapshot: snap}})
	s.send(s.seqFrame(&asterismv1.AgentFrame{SentAtMs: h.clock.Now().UnixMilli(), Payload: &asterismv1.AgentFrame_Metrics{Metrics: metrics}}))
	s.sync()
	v := h.reg.Primary()

	// --- Host: HostPage, HostStatusCard, HostTelemetryCard, PortalPage ---
	hi := v.HostSnapshot()
	str := map[string]string{
		"hostname": hi.Hostname, "os": hi.Os, "kernel": hi.Kernel, "arch": hi.Arch, "cpu_model": hi.CpuModel,
	}
	for k, x := range str {
		if x == "" {
			t.Errorf("host.%s is empty", k)
		}
	}
	if hi.SourceMode != "agent" {
		t.Errorf("host.source_mode = %q", hi.SourceMode)
	}
	nonZeroU := map[string]uint64{
		"cores": uint64(hi.Cores), "mem_total_bytes": hi.MemTotalBytes, "mem_used_bytes": hi.MemUsedBytes,
		"swap_total_bytes": hi.SwapTotalBytes, "swap_used_bytes": hi.SwapUsedBytes,
		"disk_total_bytes": hi.DiskTotalBytes, "disk_used_bytes": hi.DiskUsedBytes,
		"uptime_seconds": hi.UptimeSeconds, "boot_time_ms": uint64(hi.BootTimeMs),
	}
	for k, x := range nonZeroU {
		if x == 0 {
			t.Errorf("host.%s is zero", k)
		}
	}
	nonZeroF := map[string]float64{
		"cpu_util": hi.CpuUtil, "cpu_steal_util": hi.CpuStealUtil, "load_1": hi.Load_1, "load_5": hi.Load_5,
		"load_15": hi.Load_15, "net_rx_bps": hi.NetRxBps, "net_tx_bps": hi.NetTxBps,
	}
	for k, x := range nonZeroF {
		if x == 0 {
			t.Errorf("host.%s is zero", k)
		}
	}
	// Units: cpu.util 0-1 of the whole host (busy incl. steal), bytes/s.
	if !near(hi.CpuUtil, 0.4) || !near(hi.CpuStealUtil, 0.05) || !near(hi.NetRxBps, 400000) || !near(hi.NetTxBps, 100000) {
		t.Errorf("host units: cpu %v steal %v rx %v tx %v", hi.CpuUtil, hi.CpuStealUtil, hi.NetRxBps, hi.NetTxBps)
	}
	if hi.MemUsedBytes >= hi.MemTotalBytes || hi.DiskUsedBytes > hi.DiskTotalBytes {
		t.Errorf("host used > total: mem %d/%d disk %d/%d", hi.MemUsedBytes, hi.MemTotalBytes, hi.DiskUsedBytes, hi.DiskTotalBytes)
	}

	// --- Containers: ContainersPage, ContainerDetailPage ---
	snapD := v.DockerSnapshot()
	if !snapD.Engine.GetAvailable() || snapD.Engine.GetServerVersion() == "" {
		t.Errorf("engine %v", snapD.Engine)
	}
	var api *asterismv1.Container
	for _, c := range snapD.Containers {
		if c.Name == "shop-api-1" {
			api = c
		}
	}
	if api == nil {
		t.Fatalf("shop-api-1 missing from %d containers", len(snapD.Containers))
	}
	if api.Id != "ctr:shop-api-1" || api.ProjectId != "proj:shop" || api.Status == "" || api.UptimeSeconds == 0 ||
		api.Health == asterismv1.ContainerHealth_CONTAINER_HEALTH_UNSPECIFIED || len(api.Ports) == 0 || api.Image == "" ||
		api.StatusLevel == asterismv1.HealthStatus_HEALTH_STATUS_UNSPECIFIED || api.GetResources().GetMemLimitBytes() == 0 {
		t.Errorf("container facts: %v", api)
	}
	st := api.GetStats()
	if st == nil {
		t.Fatal("shop-api-1 has no live stats")
	}
	// cpu.util is cores (1.0 = one core) = docker stats CPU% / 100, as the
	// SSH bridge publishes it: +2.5 s of CPU in 5 s = 0.5.
	if !near(st.CpuUtil, 0.5) {
		t.Errorf("container cpu.util %v, want 0.5 cores", st.CpuUtil)
	}
	statsU := map[string]uint64{"mem_used_bytes": st.MemUsedBytes, "mem_limit_bytes": st.MemLimitBytes, "pids": uint64(st.Pids), "ts_ms": uint64(st.TsMs)}
	for k, x := range statsU {
		if x == 0 {
			t.Errorf("stats.%s is zero", k)
		}
	}
	statsF := map[string]float64{"net_rx_bps": st.NetRxBps, "net_tx_bps": st.NetTxBps, "blk_read_bps": st.BlkReadBps, "blk_write_bps": st.BlkWriteBps, "mem_util": st.MemUtil}
	for k, x := range statsF {
		if x == 0 {
			t.Errorf("stats.%s is zero", k)
		}
	}
	if !near(st.NetRxBps, 100000) || !near(st.NetTxBps, 20000) || !near(st.BlkReadBps, 4<<20/5.0) || !near(st.BlkWriteBps, 1<<20/5.0) {
		t.Errorf("container rates: %v", st)
	}

	// --- Inventory: workloads, projects (overview KPIs), ports ---
	w, ok := v.Workload("ctr:shop-api-1")
	if !ok || !near(w.CpuUtil, 0.5) || w.MemUsedBytes == 0 || w.NetRxBps == 0 {
		t.Errorf("workload %v %v", w, ok)
	}
	var shop *asterismv1.Project
	for _, p := range v.Projects() {
		if p.Id == "proj:shop" {
			shop = p
		}
	}
	if shop == nil || shop.CpuUtil == 0 || shop.MemUsedBytes == 0 || shop.MemSharePct == 0 {
		t.Errorf("project rollup %v", shop)
	}
	if len(v.Ports()) == 0 {
		t.Error("no ports")
	}
	var published bool
	for _, p := range v.Ports() {
		if p.HostPort == 18080 && p.WorkloadId == "ctr:shop-api-1" {
			published = true
		}
	}
	if !published {
		t.Errorf("published port 18080 not attributed to shop-api-1: %v", v.Ports())
	}

	// The live charts (LiveStore -> uPlot) subscribe to these bus series.
	onBus := map[string]bool{}
	for _, m := range drain().Samples {
		onBus[m.EntityID+" "+m.Metric] = true
	}
	for _, k := range []string{
		"host cpu.util", "host mem.used_bytes", "host net.rx_bps", "host net.tx_bps", "host load.1",
		"ctr:shop-api-1 cpu.util", "ctr:shop-api-1 mem.used_bytes", "ctr:shop-api-1 net.rx_bps", "ctr:shop-api-1 net.tx_bps",
		"ctr:shop-api-1 blk.read_bps", "ctr:shop-api-1 blk.write_bps", "ctr:shop-api-1 pids.count",
		"proj:shop cpu.util", "proj:shop mem.used_bytes",
	} {
		if !onBus[k] {
			t.Errorf("series %q not published to the bus", k)
		}
	}
}

func near(got, want float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d <= 1e-3*max(1, want)
}
