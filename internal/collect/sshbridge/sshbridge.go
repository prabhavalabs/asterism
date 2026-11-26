package sshbridge

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/assemble"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

// Collector gathers real, read-only Linux host, Docker, open-port, and Nginx ingress
// telemetry from a remote VPS over multiplexed system SSH.
type Collector struct {
	logger *slog.Logger
	target string
	bus    *bus.Bus
	ring   *ring.Store

	mu        sync.RWMutex
	hostInfo  *asterismv1.HostInfo
	projects  []*asterismv1.Project
	workloads map[string]*asterismv1.Workload
	ports     []*asterismv1.PortBinding
	ingress   []*asterismv1.IngressRoute
	edges     []*asterismv1.TopologyEdge
	events    []*asterismv1.Event

	prevCPUTotal uint64
	prevCPUIdle  uint64
	prevNetTS    time.Time
	prevNetRx    uint64
	prevNetTx    uint64

	// Docker inventory (guarded by mu).
	containers   []*asterismv1.Container               // from batched docker inspect
	ctrStats     map[string]*asterismv1.ContainerStats // by short container id
	images       []*asterismv1.DockerImage
	volumes      []*asterismv1.DockerVolume
	networks     []*asterismv1.DockerNetwork
	diskUsage    *asterismv1.DockerDiskUsage
	engine       *asterismv1.DockerEngine
	volumeSizes  map[string]int64
	dockerSyncMs int64

	rates   *dockercli.RateTracker // only touched by the fast-metrics loop
	refresh chan struct{}
}

func New(logger *slog.Logger, target string, b *bus.Bus, r *ring.Store) *Collector {
	t := strings.TrimSpace(target)
	return &Collector{
		logger:    logger,
		target:    t,
		bus:       b,
		ring:      r,
		workloads: make(map[string]*asterismv1.Workload),
		ctrStats:  make(map[string]*asterismv1.ContainerStats),
		rates:     dockercli.NewRateTracker(),
		refresh:   make(chan struct{}, 1),
		hostInfo: &asterismv1.HostInfo{
			Id:         "host",
			Hostname:   t,
			SourceMode: "ssh",
			SshTarget:  t,
		},
	}
}

func (c *Collector) Target() string { return c.target }

func (c *Collector) HostSnapshot() *asterismv1.HostInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return proto.Clone(c.hostInfo).(*asterismv1.HostInfo)
}

// The getters below return caller-owned deep copies. collectFastMetrics and
// ContainerAction update workloads and project rollups in place under c.mu, so
// handing out the internal pointers would let API readers race with the poll
// loops once the read lock is released.

func (c *Collector) Projects() []*asterismv1.Project {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := dockercli.CloneAll(c.projects)
	if out == nil {
		out = []*asterismv1.Project{}
	}
	return out
}

func (c *Collector) Workloads() []*asterismv1.Workload {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*asterismv1.Workload, 0, len(c.workloads))
	for _, proj := range c.projects {
		for _, w := range proj.Workloads {
			out = append(out, proto.Clone(w).(*asterismv1.Workload))
		}
	}
	return out
}

func (c *Collector) Workload(id string) (*asterismv1.Workload, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	w, ok := c.workloads[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(w).(*asterismv1.Workload), true
}

func (c *Collector) Ports() []*asterismv1.PortBinding {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := dockercli.CloneAll(c.ports)
	if out == nil {
		out = []*asterismv1.PortBinding{}
	}
	return out
}

func (c *Collector) IngressRoutes() []*asterismv1.IngressRoute {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := dockercli.CloneAll(c.ingress)
	if out == nil {
		out = []*asterismv1.IngressRoute{}
	}
	return out
}

func (c *Collector) Edges() []*asterismv1.TopologyEdge {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := dockercli.CloneAll(c.edges)
	if out == nil {
		out = []*asterismv1.TopologyEdge{}
	}
	return out
}

func (c *Collector) Events() []*asterismv1.Event {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := dockercli.CloneAll(c.events)
	if out == nil {
		out = []*asterismv1.Event{}
	}
	return out
}

// RedactLog applies the shared free-text redaction rules (URL userinfo,
// Authorization/Cookie headers, bearer tokens, secret key=value pairs) to a
// log line, so SSH-bridge logs are redacted exactly like agent logs.
func RedactLog(line string) string {
	return dockercli.RedactText(line)
}

// Logs fetches a read-only tail of container logs over SSH and redacts secret-looking tokens.
func (c *Collector) Logs(workloadID string) []*asterismv1.LogLine {
	if !strings.HasPrefix(workloadID, "ctr:") {
		return []*asterismv1.LogLine{
			{
				TsMs:       time.Now().UnixMilli(),
				WorkloadId: workloadID,
				Stream:     "stdout",
				Text:       "Host systemd unit logs available when deployed natively with journald access.",
			},
		}
	}
	ctrName := strings.TrimPrefix(workloadID, "ctr:")
	if !validContainerName(ctrName) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := c.runSSH(ctx, fmt.Sprintf("docker logs --tail 45 --timestamps %s 2>&1", ctrName))
	if err != nil {
		return []*asterismv1.LogLine{
			{
				TsMs:       time.Now().UnixMilli(),
				WorkloadId: workloadID,
				Stream:     "stderr",
				Text:       fmt.Sprintf("unable to read logs: %v", err),
			},
		}
	}
	now := time.Now().UnixMilli()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	res := make([]*asterismv1.LogLine, 0, len(lines))
	for _, raw := range lines {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		tsMs := now
		text := raw
		if sp := strings.IndexByte(raw, ' '); sp > 10 {
			if parsed, perr := time.Parse(time.RFC3339Nano, raw[:sp]); perr == nil {
				tsMs = parsed.UnixMilli()
				text = strings.TrimSpace(raw[sp+1:])
			}
		}
		res = append(res, &asterismv1.LogLine{
			TsMs:       tsMs,
			WorkloadId: workloadID,
			Stream:     "stdout",
			Text:       RedactLog(text),
		})
	}
	return res
}

func validContainerName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// Run starts the live SSH collection loops (5s metrics + 30s full inventory/nginx/ports).
func (c *Collector) Run(ctx context.Context) error {
	if c.target == "" {
		return fmt.Errorf("ssh target is empty")
	}

	if err := c.collectInventory(ctx); err != nil {
		c.logger.Warn("ssh initial inventory failed", "target", c.target, "err", err)
	}
	if err := c.collectFastMetrics(ctx); err != nil {
		c.logger.Warn("ssh initial metrics failed", "target", c.target, "err", err)
	}

	go c.runResourcesLoop(ctx)

	fastTicker := time.NewTicker(5 * time.Second)
	invTicker := time.NewTicker(30 * time.Second)
	defer fastTicker.Stop()
	defer invTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-fastTicker.C:
			if err := c.collectFastMetrics(ctx); err != nil && ctx.Err() == nil {
				c.logger.Warn("ssh metrics poll failed", "target", c.target, "err", err)
			}
		case <-invTicker.C:
			if err := c.collectInventory(ctx); err != nil && ctx.Err() == nil {
				c.logger.Warn("ssh inventory poll failed", "target", c.target, "err", err)
			}
		case <-c.refresh:
			// An action changed container state; resync topology promptly.
			if err := c.collectInventory(ctx); err != nil && ctx.Err() == nil {
				c.logger.Warn("ssh inventory refresh failed", "target", c.target, "err", err)
			}
		}
	}
}

func (c *Collector) runSSH(ctx context.Context, remoteCmd string) (string, error) {
	return c.runSSHTimeout(ctx, remoteCmd, 12*time.Second)
}

func (c *Collector) runSSHTimeout(ctx context.Context, remoteCmd string, timeout time.Duration) (string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(
		cmdCtx,
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=6",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/tmp/asterism-ssh-%C",
		"-o", "ControlPersist=60",
		c.target,
		remoteCmd,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("ssh %s: %w (%s)", c.target, err, errMsg)
		}
		return "", fmt.Errorf("ssh %s: %w", c.target, err)
	}
	return stdout.String(), nil
}

const fastMetricsScript = `
echo "@@STAT@@"
head -n 1 /proc/stat
echo "@@LOAD@@"
cat /proc/loadavg
echo "@@MEM@@"
grep -E "^(MemTotal|MemAvailable|SwapTotal|SwapFree):" /proc/meminfo
echo "@@DISK@@"
df -B1 / | tail -n 1
echo "@@UPTIME@@"
cat /proc/uptime
echo "@@NET@@"
cat /proc/net/dev
`

func (c *Collector) collectFastMetrics(ctx context.Context) error {
	c.mu.RLock()
	pids := make([]int32, 0, len(c.containers))
	idByPid := make(map[int32]string, len(c.containers))
	for _, ctr := range c.containers {
		if ctr.Pid > 0 && ownNetns(ctr.NetworkMode) {
			pids = append(pids, ctr.Pid)
			idByPid[ctr.Pid] = ctr.ContainerId
		}
	}
	c.mu.RUnlock()

	script := fastMetricsScript + "echo \"@@DSTATS@@\"\n" + dockercli.StatsCommand + "\necho \"@@CGIO@@\"\n" + dockercli.CgroupIOScript + "\n"
	if nd := dockercli.NetDevScript(pids); nd != "" {
		script += "echo \"@@CNET@@\"\n" + nd + "\n"
	}
	out, err := c.runSSH(ctx, script)
	if err != nil {
		return err
	}
	c.applyFastMetrics(splitSections(out), idByPid, time.Now())
	return nil
}

// applyFastMetrics folds one fast-metrics poll into the collector state and
// publishes the resulting samples. It runs only on the Run goroutine, which
// owns the prev* delta fields and the rate tracker.
func (c *Collector) applyFastMetrics(sections map[string]string, idByPid map[int32]string, now time.Time) {
	tsMs := now.UnixMilli()

	// CPU util from /proc/stat delta
	var cpuUtil float64
	if total, idle, ok := parseProcStat(sections["STAT"]); ok {
		if c.prevCPUTotal > 0 && total > c.prevCPUTotal {
			dt := float64(total - c.prevCPUTotal)
			di := float64(idle - c.prevCPUIdle)
			cpuUtil = clamp((dt-di)/dt, 0, 1)
		}
		c.prevCPUTotal = total
		c.prevCPUIdle = idle
	}

	load1, load5, load15 := parseLoadAvg(sections["LOAD"])
	memTotal, memUsed, swapTotal, swapUsed := parseMemInfo(sections["MEM"])
	diskTotal, diskUsed := parseDfRoot(sections["DISK"])
	uptimeSec := parseUptime(sections["UPTIME"])

	var rxBps, txBps float64
	if rx, tx, ok := parseNetDev(sections["NET"]); ok {
		if !c.prevNetTS.IsZero() {
			dt := now.Sub(c.prevNetTS).Seconds()
			if dt > 0 {
				if rx >= c.prevNetRx {
					rxBps = float64(rx-c.prevNetRx) / dt
				}
				if tx >= c.prevNetTx {
					txBps = float64(tx-c.prevNetTx) / dt
				}
			}
		}
		c.prevNetTS = now
		c.prevNetRx = rx
		c.prevNetTx = tx
	}

	stats := dockercli.ParseStats(sections["DSTATS"])
	cgIO := dockercli.ParseCgroupIO(sections["CGIO"])
	netByPid := dockercli.ParseNetDevByPid(sections["CNET"])
	netByID := make(map[string]dockercli.IOCounter, len(netByPid))
	for pid, ctr := range netByPid {
		if id, ok := idByPid[pid]; ok {
			netByID[id] = ctr
		}
	}

	samples := []bus.MetricSample{
		{EntityID: "host", Metric: "cpu.util", TSMs: tsMs, Value: cpuUtil},
		{EntityID: "host", Metric: "mem.used_bytes", TSMs: tsMs, Value: float64(memUsed)},
		{EntityID: "host", Metric: "net.rx_bps", TSMs: tsMs, Value: rxBps},
		{EntityID: "host", Metric: "net.tx_bps", TSMs: tsMs, Value: txBps},
		{EntityID: "host", Metric: "load.1", TSMs: tsMs, Value: load1},
	}

	c.mu.Lock()
	c.hostInfo.CpuUtil = cpuUtil
	if memTotal > 0 {
		c.hostInfo.MemTotalBytes = memTotal
		c.hostInfo.MemUsedBytes = memUsed
	}
	c.hostInfo.SwapTotalBytes = swapTotal
	c.hostInfo.SwapUsedBytes = swapUsed
	if diskTotal > 0 {
		c.hostInfo.DiskTotalBytes = diskTotal
		c.hostInfo.DiskUsedBytes = diskUsed
	}
	c.hostInfo.Load_1 = load1
	c.hostInfo.Load_5 = load5
	c.hostInfo.Load_15 = load15
	c.hostInfo.NetRxBps = rxBps
	c.hostInfo.NetTxBps = txBps
	if uptimeSec > 0 {
		c.hostInfo.UptimeSeconds = uptimeSec
	}

	hostMemTotal := c.hostInfo.MemTotalBytes
	if hostMemTotal == 0 {
		hostMemTotal = 1
	}

	newStats := make(map[string]*asterismv1.ContainerStats, len(stats))
	keep := make(map[string]bool, len(stats))
	for fullID, st := range stats {
		shortID := dockercli.ShortID(fullID)
		keep[shortID] = true
		cs := &asterismv1.ContainerStats{
			TsMs:          tsMs,
			CpuUtil:       st.CPUUtil,
			MemUsedBytes:  st.MemUsed,
			MemLimitBytes: st.MemLimit,
			NetRxBytes:    st.NetRx,
			NetTxBytes:    st.NetTx,
			BlkReadBytes:  st.BlkRd,
			BlkWriteBytes: st.BlkWr,
			Pids:          st.PIDs,
		}
		// Prefer exact kernel counters over Docker's rounded human-readable totals.
		if n, ok := netByID[shortID]; ok {
			cs.NetRxBytes, cs.NetTxBytes = n.Read, n.Write
		}
		if io, ok := cgIO[fullID]; ok && (io.Read > 0 || io.Write > 0) {
			cs.BlkReadBytes, cs.BlkWriteBytes = io.Read, io.Write
		}
		if cs.MemLimitBytes > 0 {
			cs.MemUtil = float64(cs.MemUsedBytes) / float64(cs.MemLimitBytes)
		}
		r := c.rates.Observe(shortID, now, cs.NetRxBytes, cs.NetTxBytes, cs.BlkReadBytes, cs.BlkWriteBytes)
		cs.NetRxBps, cs.NetTxBps, cs.BlkReadBps, cs.BlkWriteBps = r.NetRx, r.NetTx, r.BlkRd, r.BlkWr
		newStats[shortID] = cs

		entity := "ctr:" + st.Name
		if wl, ok := c.workloads[entity]; ok {
			wl.CpuUtil = st.CPUUtil
			wl.MemUsedBytes = st.MemUsed
			wl.MemLimitBytes = st.MemLimit
			wl.NetRxBps = cs.NetRxBps
			wl.NetTxBps = cs.NetTxBps
		}
		samples = append(samples,
			bus.MetricSample{EntityID: entity, Metric: "cpu.util", TSMs: tsMs, Value: cs.CpuUtil},
			bus.MetricSample{EntityID: entity, Metric: "mem.used_bytes", TSMs: tsMs, Value: float64(cs.MemUsedBytes)},
			bus.MetricSample{EntityID: entity, Metric: "net.rx_bps", TSMs: tsMs, Value: cs.NetRxBps},
			bus.MetricSample{EntityID: entity, Metric: "net.tx_bps", TSMs: tsMs, Value: cs.NetTxBps},
			bus.MetricSample{EntityID: entity, Metric: "blk.read_bps", TSMs: tsMs, Value: cs.BlkReadBps},
			bus.MetricSample{EntityID: entity, Metric: "blk.write_bps", TSMs: tsMs, Value: cs.BlkWriteBps},
			bus.MetricSample{EntityID: entity, Metric: "pids.count", TSMs: tsMs, Value: float64(cs.Pids)},
		)
	}
	c.rates.Forget(keep)
	c.ctrStats = newStats

	for _, proj := range c.projects {
		var cpuSum float64
		var memSum, limitSum uint64
		for _, wl := range proj.Workloads {
			cpuSum += wl.CpuUtil
			memSum += wl.MemUsedBytes
			limitSum += wl.MemLimitBytes
		}
		proj.CpuUtil = cpuSum
		proj.MemUsedBytes = memSum
		proj.MemLimitBytes = limitSum
		proj.MemSharePct = (float64(memSum) / float64(hostMemTotal)) * 100.0
		samples = append(samples,
			bus.MetricSample{EntityID: proj.Id, Metric: "cpu.util", TSMs: tsMs, Value: cpuSum},
			bus.MetricSample{EntityID: proj.Id, Metric: "mem.used_bytes", TSMs: tsMs, Value: float64(memSum)},
		)
	}
	c.mu.Unlock()

	c.bus.Publish(bus.Message{Samples: samples})
}

const inventoryScript = `
echo "@@HOST@@"
hostname
uname -sr
uname -m
nproc
grep -m1 "model name" /proc/cpuinfo 2>/dev/null | cut -d: -f2 || true
echo "@@DOCKER@@"
docker ps -a --format "{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Ports}}|{{.Label \"com.docker.compose.project\"}}|{{.Label \"com.docker.compose.service\"}}|{{.Label \"com.docker.compose.project.working_dir\"}}|{{.Networks}}" 2>/dev/null || true
echo "@@DINSPECT@@"
` + dockercli.InspectAllScript + `
echo "@@PORTS@@"
ss -tulpn 2>/dev/null || true
echo "@@NGINX_ACTIVE@@"
systemctl is-active nginx 2>/dev/null || true
echo "@@NGINX_SITES@@"
for f in /etc/nginx/sites-enabled/* /etc/nginx/conf.d/*.conf; do
  [ -f "$f" ] || continue
  echo "###FILE:$f"
  grep -E "^\s*(listen|server_name|proxy_pass|upstream|server\s+[0-9a-zA-Z.:_-]+|ssl_certificate\s)" "$f" 2>/dev/null | grep -v "ssl_certificate_key" || true
done
`

func (c *Collector) collectInventory(ctx context.Context) error {
	out, err := c.runSSH(ctx, inventoryScript)
	if err != nil {
		return err
	}
	c.applyInventory(splitSections(out), time.Now())
	return nil
}

// inventoryInput parses one inventory poll into assemble facts. prev carries
// live CPU and memory numbers across the rebuild.
func (c *Collector) inventoryInput(sections map[string]string, now time.Time, prev map[string]assemble.LiveUsage, hostMemTotal uint64) assemble.Input {
	nowMs := now.UnixMilli()
	in := assemble.Input{
		Now:          now,
		HostMemTotal: hostMemTotal,
		Inspected:    dockercli.ParseInspect(sections["DINSPECT"], now),
		Prev:         prev,
	}
	// Host Nginx unit as a first-class workload in proj:edge-ingress
	nginxActive := strings.TrimSpace(sections["NGINX_ACTIVE"]) == "active"
	if nginxActive {
		in.HostWorkloads = append(in.HostWorkloads, assemble.NginxUnit())
	}
	for _, ctr := range parseDockerPS(sections["DOCKER"]) {
		in.Containers = append(in.Containers, assemble.ContainerFact{
			Name: ctr.name, Image: ctr.image, Status: ctr.status, PortsRaw: ctr.portsRaw,
			Project: ctr.project, Service: ctr.service, WorkingDir: ctr.workingDir, Networks: ctr.networks,
		})
	}
	for _, rp := range parseSSPorts(sections["PORTS"]) {
		in.Listeners = append(in.Listeners, assemble.Listener{Proto: rp.proto, HostIP: rp.hostIP, Port: rp.port, Process: rp.process})
	}
	for _, site := range parseNginxSites(sections["NGINX_SITES"]) {
		r := assemble.ProxyRoute{
			Kind:            asterismv1.ProxyKind_PROXY_KIND_NGINX,
			ProxyWorkloadID: assemble.NginxUnitID,
			Hostnames:       site.hostnames,
			ListenPort:      site.listenPort,
			TLS:             site.tls,
			PathPrefix:      "/",
		}
		for _, p := range site.targetPorts {
			r.Upstreams = append(r.Upstreams, fmt.Sprintf("127.0.0.1:%d", p))
		}
		in.Routes = append(in.Routes, r)
	}
	target := c.target
	in.Sync = func(projects, workloads, routes int) *asterismv1.Event {
		return &asterismv1.Event{
			Id:         "evt:ssh:sync",
			TsMs:       nowMs,
			EntityId:   "host",
			EntityName: target,
			Kind:       "ssh.sync",
			Severity:   "info",
			Message:    fmt.Sprintf("Synced %d projects, %d workloads, %d Nginx routes over SSH", projects, workloads, routes),
		}
	}
	return in
}

// applyInventory assembles one inventory poll and stores the result.
func (c *Collector) applyInventory(sections map[string]string, now time.Time) {
	hostLines := nonEmptyLines(sections["HOST"])

	// Preserve existing live CPU/mem numbers across inventory refreshes. Copy the
	// values while holding the lock: the workloads themselves keep being updated
	// by ContainerAction after it is released.
	c.mu.RLock()
	prevWorkloads := make(map[string]assemble.LiveUsage, len(c.workloads))
	for k, v := range c.workloads {
		prevWorkloads[k] = assemble.LiveUsage{CPUUtil: v.CpuUtil, MemUsedBytes: v.MemUsedBytes, MemLimitBytes: v.MemLimitBytes}
	}
	hostMemTotal := c.hostInfo.MemTotalBytes
	c.mu.RUnlock()

	out := assemble.Build(c.inventoryInput(sections, now, prevWorkloads, hostMemTotal))

	c.mu.Lock()
	if len(hostLines) >= 1 {
		c.hostInfo.Hostname = hostLines[0]
	}
	if len(hostLines) >= 2 {
		c.hostInfo.Os = hostLines[1]
		c.hostInfo.Kernel = hostLines[1]
	}
	if len(hostLines) >= 3 {
		c.hostInfo.Arch = hostLines[2]
	}
	if len(hostLines) >= 4 {
		if cores, err := strconv.Atoi(hostLines[3]); err == nil && cores > 0 {
			c.hostInfo.Cores = uint32(cores)
		}
	}
	if len(hostLines) >= 5 {
		c.hostInfo.CpuModel = strings.TrimSpace(hostLines[4])
	}
	c.projects = out.Projects
	c.workloads = out.Workloads
	c.containers = out.Containers
	c.dockerSyncMs = now.UnixMilli()
	c.ports = out.Ports
	c.ingress = out.Ingress
	c.edges = out.Edges
	c.events = out.Events
	c.mu.Unlock()
}

// --- Parsers ---

func splitSections(out string) map[string]string {
	res := make(map[string]string)
	var curKey string
	var buf strings.Builder
	for _, line := range strings.Split(out, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "@@") && strings.HasSuffix(trim, "@@") && len(trim) > 4 {
			if curKey != "" {
				res[curKey] = buf.String()
			}
			curKey = trim[2 : len(trim)-2]
			buf.Reset()
			continue
		}
		if curKey != "" {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	if curKey != "" {
		res[curKey] = buf.String()
	}
	return res
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func parseProcStat(s string) (total, idle uint64, ok bool) {
	for _, line := range nonEmptyLines(s) {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		if len(fields) < 4 {
			return 0, 0, false
		}
		for i, f := range fields {
			v, _ := strconv.ParseUint(f, 10, 64)
			total += v
			if i == 3 || i == 4 { // idle + iowait
				idle += v
			}
		}
		return total, idle, true
	}
	return 0, 0, false
}

func parseLoadAvg(s string) (l1, l5, l15 float64) {
	fields := strings.Fields(s)
	if len(fields) >= 3 {
		l1, _ = strconv.ParseFloat(fields[0], 64)
		l5, _ = strconv.ParseFloat(fields[1], 64)
		l15, _ = strconv.ParseFloat(fields[2], 64)
	}
	return
}

func parseMemInfo(s string) (memTotal, memUsed, swapTotal, swapUsed uint64) {
	var memAvail, swapFree uint64
	for _, line := range nonEmptyLines(s) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		vBytes := v * 1024
		switch fields[0] {
		case "MemTotal:":
			memTotal = vBytes
		case "MemAvailable:":
			memAvail = vBytes
		case "SwapTotal:":
			swapTotal = vBytes
		case "SwapFree:":
			swapFree = vBytes
		}
	}
	if memTotal >= memAvail {
		memUsed = memTotal - memAvail
	}
	if swapTotal >= swapFree {
		swapUsed = swapTotal - swapFree
	}
	return
}

func parseDfRoot(s string) (total, used uint64) {
	lines := nonEmptyLines(s)
	if len(lines) == 0 {
		return 0, 0
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) >= 3 {
		total, _ = strconv.ParseUint(fields[1], 10, 64)
		used, _ = strconv.ParseUint(fields[2], 10, 64)
	}
	return
}

func parseUptime(s string) uint64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	f, _ := strconv.ParseFloat(fields[0], 64)
	return uint64(f)
}

func parseNetDev(s string) (rx, tx uint64, ok bool) {
	for _, line := range nonEmptyLines(s) {
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:colon])
		if iface == "lo" || strings.HasPrefix(iface, "veth") || strings.HasPrefix(iface, "br-") || strings.HasPrefix(iface, "docker") {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) >= 9 {
			r, _ := strconv.ParseUint(fields[0], 10, 64)
			t, _ := strconv.ParseUint(fields[8], 10, 64)
			rx += r
			tx += t
			ok = true
		}
	}
	return
}

type rawContainer struct {
	id         string
	name       string
	image      string
	status     string
	portsRaw   string
	project    string
	service    string
	workingDir string
	networks   []string
}

func parseDockerPS(s string) []rawContainer {
	var out []rawContainer
	for _, line := range nonEmptyLines(s) {
		parts := strings.Split(line, "|")
		if len(parts) < 9 {
			continue
		}
		var nets []string
		for _, n := range strings.Split(parts[8], ",") {
			if t := strings.TrimSpace(n); t != "" {
				nets = append(nets, t)
			}
		}
		out = append(out, rawContainer{
			id:         parts[0],
			name:       parts[1],
			image:      parts[2],
			status:     parts[3],
			portsRaw:   parts[4],
			project:    parts[5],
			service:    parts[6],
			workingDir: parts[7],
			networks:   nets,
		})
	}
	return out
}

// parseDockerStatus and parseDockerPorts are kept for the action path and
// tests; classification itself lives in internal/collect/assemble.
func parseDockerStatus(status string) (asterismv1.HealthStatus, string) {
	return assemble.ParseDockerStatus(status)
}

func parseDockerPorts(raw, wlID, wlName, projID string) []*asterismv1.PortBinding {
	return assemble.ParseDockerPorts(raw, wlID, wlName, projID)
}

type rawSSPort struct {
	proto   string
	hostIP  string
	port    uint32
	process string
}

var ssProcRe = regexp.MustCompile(`users:\(\("([^"]+)"`)

func parseSSPorts(s string) []rawSSPort {
	var out []rawSSPort
	for _, line := range nonEmptyLines(s) {
		if strings.HasPrefix(line, "Netid") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		proto := fields[0]
		state := fields[1]
		if proto == "tcp" && state != "LISTEN" {
			continue
		}
		addr := fields[4]
		lastColon := strings.LastIndexByte(addr, ':')
		if lastColon < 0 {
			continue
		}
		ip := addr[:lastColon]
		if pct := strings.IndexByte(ip, '%'); pct >= 0 {
			ip = ip[:pct]
		}
		portNum, err := strconv.Atoi(addr[lastColon+1:])
		if err != nil || portNum <= 0 {
			continue
		}
		proc := "host"
		if m := ssProcRe.FindStringSubmatch(line); len(m) == 2 {
			proc = m[1]
		}
		out = append(out, rawSSPort{
			proto:   proto,
			hostIP:  ip,
			port:    uint32(portNum),
			process: proc,
		})
	}
	return out
}

type nginxSite struct {
	file        string
	hostnames   []string
	listenPort  uint32
	tls         bool
	targetPorts []uint32
}

var proxyPassPortRe = regexp.MustCompile(`https?://(?:127\.0\.0\.1|localhost|\[::1\]):(\d+)`)

func parseNginxSites(s string) []nginxSite {
	var sites []nginxSite
	var cur *nginxSite
	seenHost := make(map[string]bool)
	seenTarget := make(map[uint32]bool)

	flush := func() {
		if cur != nil && len(cur.hostnames) > 0 {
			if cur.listenPort == 0 {
				if cur.tls {
					cur.listenPort = 443
				} else {
					cur.listenPort = 80
				}
			}
			sites = append(sites, *cur)
		}
		cur = nil
		seenHost = make(map[string]bool)
		seenTarget = make(map[uint32]bool)
	}

	for _, line := range nonEmptyLines(s) {
		if strings.HasPrefix(line, "###FILE:") {
			flush()
			cur = &nginxSite{file: strings.TrimPrefix(line, "###FILE:")}
			continue
		}
		if cur == nil {
			continue
		}
		trim := strings.TrimSuffix(strings.TrimSpace(line), ";")
		fields := strings.Fields(trim)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "server_name":
			for _, h := range fields[1:] {
				if h == "_" || h == "localhost" || seenHost[h] {
					continue
				}
				seenHost[h] = true
				cur.hostnames = append(cur.hostnames, h)
			}
		case "listen":
			if strings.Contains(trim, "443") || strings.Contains(trim, "ssl") {
				cur.tls = true
				cur.listenPort = 443
			} else if cur.listenPort == 0 && strings.Contains(trim, "80") {
				cur.listenPort = 80
			}
		case "ssl_certificate":
			cur.tls = true
		case "proxy_pass":
			if m := proxyPassPortRe.FindStringSubmatch(trim); len(m) == 2 {
				p, _ := strconv.Atoi(m[1])
				if p > 0 && !seenTarget[uint32(p)] {
					seenTarget[uint32(p)] = true
					cur.targetPorts = append(cur.targetPorts, uint32(p))
				}
			}
		}
	}
	flush()
	return sites
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
