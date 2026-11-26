package host

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gopsnet "github.com/shirou/gopsutil/v4/net"
	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/bus"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Collector gathers live host vitals via gopsutil v4 at 1s intervals.
type Collector struct {
	logger     *slog.Logger
	bus        *bus.Bus
	sourceMode string
	sshTarget  string

	mu       sync.RWMutex
	snapshot *asterismv1.HostInfo

	prevNetTS time.Time
	prevRx    uint64
	prevTx    uint64
}

func New(logger *slog.Logger, b *bus.Bus, sourceMode, sshTarget string) *Collector {
	return &Collector{
		logger:     logger,
		bus:        b,
		sourceMode: sourceMode,
		sshTarget:  sshTarget,
		snapshot: &asterismv1.HostInfo{
			Id:         "host",
			Hostname:   "localhost",
			Os:         runtime.GOOS,
			Arch:       runtime.GOARCH,
			Cores:      uint32(runtime.NumCPU()),
			SourceMode: sourceMode,
			SshTarget:  sshTarget,
		},
	}
}

func (c *Collector) HostSnapshot() *asterismv1.HostInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return proto.Clone(c.snapshot).(*asterismv1.HostInfo)
}

func (c *Collector) Run(ctx context.Context) error {
	c.collectStatic(ctx)
	c.collectTick(ctx)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.collectTick(ctx)
		}
	}
}

func (c *Collector) collectStatic(ctx context.Context) {
	info, err := host.InfoWithContext(ctx)
	cpuInfos, _ := cpu.InfoWithContext(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil && info != nil {
		c.snapshot.Hostname = info.Hostname
		c.snapshot.Os = fmt.Sprintf("%s %s", info.Platform, info.PlatformVersion)
		c.snapshot.Kernel = info.KernelVersion
		c.snapshot.Arch = info.KernelArch
		c.snapshot.UptimeSeconds = info.Uptime
		c.snapshot.BootTimeMs = int64(info.BootTime) * 1000
	}
	if len(cpuInfos) > 0 {
		c.snapshot.CpuModel = cpuInfos[0].ModelName
	}
	if count, err := cpu.CountsWithContext(ctx, true); err == nil && count > 0 {
		c.snapshot.Cores = uint32(count)
	}
}

func (c *Collector) collectTick(ctx context.Context) {
	now := time.Now()
	tsMs := now.UnixMilli()

	var cpuUtil float64
	if pcts, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(pcts) > 0 {
		cpuUtil = pcts[0] / 100.0
	}

	var memUsed, memTotal, swapUsed, swapTotal uint64
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil && vm != nil {
		memUsed = vm.Used
		memTotal = vm.Total
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil && sw != nil {
		swapUsed = sw.Used
		swapTotal = sw.Total
	}

	var diskUsed, diskTotal uint64
	if du, err := disk.UsageWithContext(ctx, "/"); err == nil && du != nil {
		diskUsed = du.Used
		diskTotal = du.Total
	}

	var load1, load5, load15 float64
	if avg, err := load.AvgWithContext(ctx); err == nil && avg != nil {
		load1 = avg.Load1
		load5 = avg.Load5
		load15 = avg.Load15
	}

	var rxBps, txBps float64
	if counters, err := gopsnet.IOCountersWithContext(ctx, false); err == nil && len(counters) > 0 {
		rx := counters[0].BytesRecv
		tx := counters[0].BytesSent
		if !c.prevNetTS.IsZero() {
			dt := now.Sub(c.prevNetTS).Seconds()
			if dt > 0 {
				if rx >= c.prevRx {
					rxBps = float64(rx-c.prevRx) / dt
				}
				if tx >= c.prevTx {
					txBps = float64(tx-c.prevTx) / dt
				}
			}
		}
		c.prevNetTS = now
		c.prevRx = rx
		c.prevTx = tx
	}

	var uptime uint64
	if up, err := host.UptimeWithContext(ctx); err == nil {
		uptime = up
	}

	c.mu.Lock()
	c.snapshot.CpuUtil = cpuUtil
	c.snapshot.MemUsedBytes = memUsed
	c.snapshot.MemTotalBytes = memTotal
	c.snapshot.SwapUsedBytes = swapUsed
	c.snapshot.SwapTotalBytes = swapTotal
	c.snapshot.DiskUsedBytes = diskUsed
	c.snapshot.DiskTotalBytes = diskTotal
	c.snapshot.Load_1 = load1
	c.snapshot.Load_5 = load5
	c.snapshot.Load_15 = load15
	c.snapshot.NetRxBps = rxBps
	c.snapshot.NetTxBps = txBps
	if uptime > 0 {
		c.snapshot.UptimeSeconds = uptime
	}
	c.mu.Unlock()

	memRatio := 0.0
	if memTotal > 0 {
		memRatio = float64(memUsed) / float64(memTotal)
	}
	c.bus.Publish(bus.Message{
		Samples: []bus.MetricSample{
			{EntityID: "host", Metric: "cpu.util", TSMs: tsMs, Value: cpuUtil},
			{EntityID: "host", Metric: "mem.used_bytes", TSMs: tsMs, Value: float64(memUsed)},
			{EntityID: "host", Metric: "mem.used_ratio", TSMs: tsMs, Value: memRatio},
			{EntityID: "host", Metric: "net.rx_bps", TSMs: tsMs, Value: rxBps},
			{EntityID: "host", Metric: "net.tx_bps", TSMs: tsMs, Value: txBps},
			{EntityID: "host", Metric: "load.1", TSMs: tsMs, Value: load1},
		},
	})
}
