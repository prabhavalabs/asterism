package host

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prabhavalabs/asterism/internal/agent/collect/collecttest"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/state"
)

func TestHostTick(t *testing.T) {
	root := collecttest.CopyTree(t, "../../procfs/testdata/root")
	cfg := config.Default()
	cfg.Host.Root = root
	cfg.Host.Filesystems = []string{"/", "/data"}
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(cfg, 2*time.Second)
	if _, err := s.Init(context.Background(), state.Env{Config: cfg, Root: root}); err != nil {
		t.Fatal(err)
	}
	h := s.HostInfo()
	if h.Os != "Ubuntu 24.04.3 LTS" || h.Kernel != "6.8.0-88-generic" || h.CpuModel != "AMD EPYC 7763 64-Core Processor" || h.Cores != 2 ||
		h.MemTotalBytes != 8130000*1024 || h.SwapTotalBytes != 2097148*1024 || h.BootTimeMs != 1795500000000 || h.SourceMode != "agent" {
		t.Fatalf("host info %v", h)
	}
	sink := collecttest.New()
	t0 := time.Unix(1_800_000_000, 0)
	if err := s.Tick(t0, sink); err != nil {
		t.Fatal(err)
	}
	if sink.Host == nil || sink.CallCount("SetHostInfo") != 1 {
		t.Fatal("HostInfo not sent once")
	}
	if v, _ := sink.Value("host", "mem.used_bytes"); v != float32((8130000-5130000)*1024) {
		t.Fatalf("mem %v", v)
	}
	if v, _ := sink.Value("host", "load.15"); math.Abs(float64(v)-0.59) > 1e-6 {
		t.Fatalf("load15 %v", v)
	}
	if _, ok := sink.Value("host", "cpu.util"); ok {
		t.Fatal("cpu util on first sample")
	}
	if len(sink.FS) != 2 || sink.FS[0].Mount != "/" || sink.FS[0].TotalBytes == 0 {
		t.Fatalf("filesystems %v", sink.FS)
	}
	if _, ok := sink.Value("host", "disk.used_ratio"); !ok {
		t.Fatal("disk.used_ratio")
	}
	if _, ok := sink.Value("host", "disk.used_ratio{/data}"); !ok {
		t.Fatalf("per-mount ratio missing: %v", sink.Keys())
	}

	// Advance counters: 400 busy + 600 idle ticks (50 steal), +2 MB on eth0,
	// +1 MB on veth (excluded), +2000 sectors read on sda.
	write := func(p, c string) {
		if err := os.WriteFile(filepath.Join(root, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/stat", "cpu  10132503 290696 3084719 46829083 16683 0 25195 1050 0 0\nbtime 1795500000\n")
	nd, _ := os.ReadFile(filepath.Join(root, "proc/1/net/dev"))
	n := strings.Replace(string(nd), "4000000000", "4002000000", 1)
	n = strings.Replace(n, "123456", "1123456", 1)
	write("proc/1/net/dev", n)
	ds, _ := os.ReadFile(filepath.Join(root, "proc/diskstats"))
	write("proc/diskstats", strings.Replace(string(ds), "sda 15000 2000 1200000", "sda 15000 2000 1202000", 1))
	sink.Reset()
	if err := s.Tick(t0.Add(2*time.Second), sink); err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"host|cpu.util":     0.4,
		"host|cpu.steal":    0.05,
		"host|net.rx_bps":   1e6,
		"host|net.tx_bps":   0,
		"host|io.read_bps":  2000 * 512 / 2,
		"host|io.write_bps": 0,
	}
	for k, w := range want {
		if g, ok := sink.Values[k]; !ok || math.Abs(float64(g)-w) > 1e-3*math.Max(1, w) {
			t.Errorf("%s = %v (%v) want %v", k, g, ok, w)
		}
	}
	if sink.CallCount("ReplaceFilesystems") != 1 {
		t.Fatalf("unchanged filesystems re-sent: %d", sink.CallCount("ReplaceFilesystems"))
	}
	_ = s.Tick(t0.Add(fsRefresh+time.Second), sink)
	if sink.CallCount("ReplaceFilesystems") != 2 {
		t.Fatal("filesystems not refreshed after fsRefresh")
	}
}

func TestInitFailsWithoutProcfs(t *testing.T) {
	s := New(config.Default(), time.Second)
	if _, err := s.Init(context.Background(), state.Env{Root: t.TempDir()}); err == nil {
		t.Fatal("init without /proc/stat must fail")
	}
}

func TestHelpers(t *testing.T) {
	m, n := cpuInfo([]byte("processor : 0\nModel : Raspberry Pi 4\nprocessor : 1\n"))
	if m != "Raspberry Pi 4" || n != 2 {
		t.Fatal(m, n)
	}
	if unameArch("amd64") != "x86_64" || unameArch("arm64") != "aarch64" {
		t.Fatal("arch")
	}
	c := statfsCandidates(state.Env{Root: "/host"}, "/")
	if len(c) != 3 || c[0] != "/host/proc/1/root" || c[2] != "/host" {
		t.Fatalf("%v", c)
	}
}
