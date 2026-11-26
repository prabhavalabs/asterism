package agentingest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/agent/state"
	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/assemble"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	"github.com/prabhavalabs/asterism/internal/collect/favicon"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/iconscan"
)

// Metric names the hub folds into host and container records. They are the
// SSH bridge's names, so charts and the ring store need no special cases:
//
//	host:   cpu.util cpu.steal mem.used_bytes mem.total_bytes swap.used_bytes
//	        swap.total_bytes load.1 load.5 load.15 net.rx_bps net.tx_bps
//	        uptime.seconds disk.used_bytes disk.total_bytes disk.used_ratio
//	        (asterism-agent sends totals, boot time and filesystem sizes as
//	        HostInfo and FilesystemUsage inventory, not as series; see
//	        TestAgentHubContract)
//	ctr/unit/proc: cpu.util mem.used_bytes mem.limit_bytes net.rx_bps
//	        net.tx_bps blk.read_bps blk.write_bps (io.read_bps/io.write_bps
//	        accepted as aliases) pids.count net.rx_bytes net.tx_bytes
//	        blk.read_bytes blk.write_bytes
//
// Every series is published to the bus whether or not it is folded.

const maxViewEvents = 500

type liveEntity struct {
	tsMs int64
	vals map[string]float64
}

// HostView is the assembled, read-only view of one agent host. Every getter
// returns caller-owned deep copies.
type HostView struct {
	hs *hostState

	mu          sync.RWMutex
	hello       *asterismv1.HostInfo
	host        *asterismv1.HostInfo
	filesystems []*asterismv1.FilesystemUsage
	out         assemble.Output
	containers  []*asterismv1.Container
	engine      *asterismv1.DockerEngine
	images      []*asterismv1.DockerImage
	volumes     []*asterismv1.DockerVolume
	networks    []*asterismv1.DockerNetwork
	diskUsage   *asterismv1.DockerDiskUsage
	syncedMs    int64
	invVersion  uint64
	live        map[string]*liveEntity
	events      []*asterismv1.Event
	// icons are favicon files the agent found, keyed by working directory.
	icons map[string]*asterismv1.ProjectIconRef
}

func newHostView(hs *hostState) *HostView {
	return &HostView{hs: hs, live: map[string]*liveEntity{}, out: assemble.Output{Workloads: map[string]*asterismv1.Workload{}}}
}

// Host returns the host id of this view.
func (v *HostView) Host() string { return v.hs.agent.Name }

func (v *HostView) setHello(h *asterismv1.AgentHello) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if h.GetHost() != nil {
		v.hello = proto.Clone(h.GetHost()).(*asterismv1.HostInfo)
	}
}

// rebuild re-assembles the topology from the session inventory. inv is
// owned by the session; everything kept here is cloned.
func (v *HostView) rebuild(inv *state.Inventory, now time.Time) {
	containers := make([]*asterismv1.Container, 0, len(inv.Containers))
	for _, id := range dockercli.SortedKeys(inv.Containers) {
		c := proto.Clone(inv.Containers[id]).(*asterismv1.Container)
		if c.Id == "" {
			c.Id = "ctr:" + c.Name
		}
		if c.ProjectId == "" {
			proj := c.ComposeProject
			if proj == "" {
				proj = "standalone"
			}
			c.ProjectId = "proj:" + proj
		}
		c.StatusLevel = dockercli.StatusLevel(c.State, c.Health)
		scrubContainer(c)
		containers = append(containers, c)
	}

	v.mu.RLock()
	prev := make(map[string]assemble.LiveUsage, len(v.live))
	for id, le := range v.live {
		prev[id] = assemble.LiveUsage{CPUUtil: le.vals["cpu.util"], MemUsedBytes: uint64(le.vals["mem.used_bytes"]), MemLimitBytes: uint64(le.vals["mem.limit_bytes"])}
	}
	base := v.hello
	v.mu.RUnlock()
	if inv.Host != nil {
		base = inv.Host
	}
	var memTotal uint64
	if base != nil {
		memTotal = base.GetMemTotalBytes()
	}

	in := assemble.Input{
		Now:          now,
		HostMemTotal: memTotal,
		Inspected:    containers,
		Prev:         prev,
		Rules:        cloneList(inv.Rules),
	}
	for _, c := range containers {
		in.Containers = append(in.Containers, assemble.FromContainer(c, now))
	}

	// Units and processes own the listeners the agent attributed to them.
	owned := map[string][]*asterismv1.PortBinding{}
	for _, l := range inv.Listeners {
		if w := l.GetWorkloadId(); strings.HasPrefix(w, "unit:") || strings.HasPrefix(w, "proc:") {
			if _, ok := inv.Workloads[w]; ok {
				owned[w] = append(owned[w], hostPort(l))
				continue
			}
		}
		in.Listeners = append(in.Listeners, assemble.Listener{
			Proto: orDefault(l.GetProto(), "tcp"), HostIP: l.GetHostIp(), Port: l.GetHostPort(), Process: l.GetProcessName(),
		})
	}
	ids := dockercli.SortedKeys(inv.Workloads)
	// Proxy units first so their listeners win the port de-duplication.
	sort.SliceStable(ids, func(i, j int) bool { return isProxyUnit(inv.Workloads[ids[i]]) && !isProxyUnit(inv.Workloads[ids[j]]) })
	for _, id := range ids {
		w := proto.Clone(inv.Workloads[id]).(*asterismv1.Workload)
		if w.Id == "" {
			continue
		}
		if w.Kind == asterismv1.WorkloadKind_WORKLOAD_KIND_UNSPECIFIED {
			w.Kind = kindFromID(w.Id)
		}
		w.Labels, _ = dockercli.FilterLabels(w.Labels)
		w.Ports = append(w.Ports, owned[w.Id]...)
		in.HostWorkloads = append(in.HostWorkloads, assemble.PlaceHostWorkload(w))
	}

	byID := map[string]*asterismv1.Workload{}
	for _, hw := range in.HostWorkloads {
		byID[hw.Workload.Id] = hw.Workload
	}
	traefikSeen := false
	for _, r := range inv.Routes {
		pr := assemble.ProxyRoute{
			Kind: r.GetProxyKind(), ProxyWorkloadID: r.GetProxyWorkloadId(), Hostnames: append([]string(nil), r.GetHostnames()...),
			ListenPort: r.GetListenPort(), TLS: r.GetTls(), PathPrefix: r.GetPathPrefix(),
		}
		if pr.ProxyWorkloadID == "" {
			pr.ProxyWorkloadID = guessProxy(r.GetProxyKind(), byID, containers)
		}
		for _, u := range r.GetUpstreams() {
			if u.GetAddress() != "" {
				pr.Upstreams = append(pr.Upstreams, u.GetAddress())
			}
		}
		if r.GetProxyKind() == asterismv1.ProxyKind_PROXY_KIND_TRAEFIK {
			traefikSeen = true
		}
		in.Routes = append(in.Routes, pr)
	}
	if !traefikSeen {
		in.Routes = append(in.Routes, assemble.TraefikRoutes(containers)...)
	}
	host := v.hs.agent.Name
	version := inv.Version
	in.Sync = func(projects, workloads, routes int) *asterismv1.Event {
		return &asterismv1.Event{
			Id: "evt:agent:sync", TsMs: now.UnixMilli(), EntityId: "host", EntityName: host,
			Kind: "agent.sync", Severity: "info",
			Message: fmt.Sprintf("Synced %d projects, %d workloads, %d ingress routes from agent %s (inventory v%d)", projects, workloads, routes, host, version),
		}
	}

	out := assemble.Build(in)

	v.mu.Lock()
	defer v.mu.Unlock()
	for id, w := range out.Workloads {
		if le, ok := v.live[id]; ok {
			foldWorkload(w, le)
		}
	}
	v.out = out
	v.containers = out.Containers
	v.host = cloneMsg(inv.Host)
	v.filesystems = cloneList(inv.Filesystems)
	v.icons = make(map[string]*asterismv1.ProjectIconRef, len(inv.Icons))
	for _, ic := range inv.Icons {
		if len(v.icons) >= 256 || ic.GetWorkingDir() == "" || len(ic.GetSha256()) != 64 || ic.GetSizeBytes() > iconscan.MaxBytes {
			continue
		}
		v.icons[ic.GetWorkingDir()] = cloneMsg(ic)
	}
	v.engine = cloneMsg(inv.Engine)
	v.images = cloneList(inv.Images)
	v.volumes = cloneList(inv.Volumes)
	v.networks = cloneList(inv.Networks)
	v.diskUsage = cloneMsg(inv.DiskUsage)
	v.syncedMs = now.UnixMilli()
	v.invVersion = inv.Version
}

// scrubContainer re-applies the agent's redaction on the hub (defence in
// depth against an outdated or buggy agent): secret-looking labels are
// dropped and secret-looking arguments redacted. Environment values are
// never part of the wire format; only names (env_keys) are.
func scrubContainer(c *asterismv1.Container) {
	var dropped uint32
	c.Labels, dropped = dockercli.FilterLabels(c.Labels)
	c.RedactedLabelCount += dropped
	c.Command = dockercli.RedactShellArgs(c.Command)
	c.Entrypoint = dockercli.RedactShellArgs(c.Entrypoint)
	if c.Healthcheck != nil {
		c.Healthcheck.Test = dockercli.RedactShellArgs(c.Healthcheck.Test)
	}
	c.Error = dockercli.RedactText(c.Error)
	keys := c.EnvKeys[:0]
	for _, k := range c.EnvKeys {
		if i := strings.IndexByte(k, '='); i >= 0 {
			k = k[:i] // never keep a value, even if an agent sent one
		}
		if k != "" {
			keys = append(keys, k)
		}
	}
	c.EnvKeys = keys
}

func isProxyUnit(w *asterismv1.Workload) bool {
	return strings.HasPrefix(w.GetId(), "unit:") && (w.GetRole() == asterismv1.WorkloadRole_WORKLOAD_ROLE_PROXY ||
		assemble.ClassifyWorkloadRole(w.GetName(), w.GetServiceName(), w.GetImage(), "") == asterismv1.WorkloadRole_WORKLOAD_ROLE_PROXY)
}

func kindFromID(id string) asterismv1.WorkloadKind {
	switch {
	case strings.HasPrefix(id, "unit:"):
		return asterismv1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT
	case strings.HasPrefix(id, "ctr:"):
		return asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER
	default:
		return asterismv1.WorkloadKind_WORKLOAD_KIND_PROCESS
	}
}

func hostPort(l *asterismv1.PortBinding) *asterismv1.PortBinding {
	p := proto.Clone(l).(*asterismv1.PortBinding)
	p.Proto = orDefault(p.Proto, "tcp")
	if p.Id == "" {
		p.Id = fmt.Sprintf("port:host:%s:%s:%d", p.Proto, p.HostIp, p.HostPort)
	}
	if p.ContainerPort == 0 {
		p.ContainerPort = p.HostPort
	}
	if p.Exposure == asterismv1.PortExposure_PORT_EXPOSURE_UNSPECIFIED {
		p.Exposure = dockercli.ClassifyExposure(p.HostIp)
	}
	if p.WorkloadName == "" {
		p.WorkloadName = p.ProcessName
	}
	return p
}

// guessProxy picks the proxy workload of a route the agent sent without one.
func guessProxy(kind asterismv1.ProxyKind, hostWorkloads map[string]*asterismv1.Workload, cs []*asterismv1.Container) string {
	slug := strings.ToLower(strings.TrimPrefix(kind.String(), "PROXY_KIND_"))
	if slug == "unspecified" {
		return ""
	}
	if _, ok := hostWorkloads["unit:"+slug+".service"]; ok {
		return "unit:" + slug + ".service"
	}
	for _, id := range dockercli.SortedKeys(hostWorkloads) {
		if strings.HasPrefix(id, "unit:"+slug) {
			return id
		}
	}
	for _, c := range cs {
		if strings.Contains(strings.ToLower(c.GetImage()), slug) {
			return c.GetId()
		}
	}
	return ""
}

// applyMetrics folds decoded points into live host, workload, and project
// values and returns project rollup samples (cpu.util, mem.used_bytes), as
// the SSH bridge publishes them.
func (v *HostView) applyMetrics(pts []state.Point) []bus.MetricSample {
	v.mu.Lock()
	defer v.mu.Unlock()
	touched := map[string]bool{}
	var ts int64
	for _, p := range pts {
		le := v.live[p.Key.Entity]
		if le == nil {
			le = &liveEntity{vals: map[string]float64{}}
			v.live[p.Key.Entity] = le
		}
		le.vals[p.Key.Metric] = float64(p.Value)
		le.tsMs = max(le.tsMs, p.TsMs)
		touched[p.Key.Entity] = true
		ts = max(ts, p.TsMs)
	}
	projTouched := map[string]bool{}
	for id := range touched {
		if w, ok := v.out.Workloads[id]; ok {
			foldWorkload(w, v.live[id])
			projTouched[w.ProjectId] = true
		}
	}
	if len(projTouched) == 0 {
		return nil
	}
	memTotal := v.memTotalLocked()
	var out []bus.MetricSample
	for _, proj := range v.out.Projects {
		if !projTouched[proj.Id] {
			continue
		}
		var cpuSum float64
		var memSum, limitSum uint64
		for _, wl := range proj.Workloads {
			cpuSum += wl.CpuUtil
			memSum += wl.MemUsedBytes
			limitSum += wl.MemLimitBytes
		}
		proj.CpuUtil, proj.MemUsedBytes, proj.MemLimitBytes = cpuSum, memSum, limitSum
		if memTotal > 0 {
			proj.MemSharePct = float64(memSum) / float64(memTotal) * 100
		}
		out = append(out,
			bus.MetricSample{EntityID: proj.Id, Metric: "cpu.util", TSMs: ts, Value: cpuSum},
			bus.MetricSample{EntityID: proj.Id, Metric: "mem.used_bytes", TSMs: ts, Value: float64(memSum)},
		)
	}
	return out
}

func foldWorkload(w *asterismv1.Workload, le *liveEntity) {
	if le == nil {
		return
	}
	if x, ok := le.vals["cpu.util"]; ok {
		w.CpuUtil = x
	}
	if x, ok := le.vals["mem.used_bytes"]; ok {
		w.MemUsedBytes = uint64(x)
	}
	if x, ok := le.vals["mem.limit_bytes"]; ok && x > 0 {
		w.MemLimitBytes = uint64(x)
	}
	if x, ok := le.vals["net.rx_bps"]; ok {
		w.NetRxBps = x
	}
	if x, ok := le.vals["net.tx_bps"]; ok {
		w.NetTxBps = x
	}
}

func (v *HostView) memTotalLocked() uint64 {
	if h := v.host; h != nil && h.MemTotalBytes > 0 {
		return h.MemTotalBytes
	}
	if v.hello != nil && v.hello.MemTotalBytes > 0 {
		return v.hello.MemTotalBytes
	}
	if le := v.live["host"]; le != nil {
		return uint64(le.vals["mem.total_bytes"])
	}
	return 0
}

// addEvents stores agent events (unit.failed, process.exit, ...) and
// returns the copies to publish, with qualified entity ids and hub-time
// timestamps.
func (v *HostView) addEvents(in []*asterismv1.Event, skewAdjMs int64) []*asterismv1.Event {
	out := make([]*asterismv1.Event, 0, len(in))
	host := v.hs.agent.Name
	for i, e := range in {
		if i >= 100 {
			break
		}
		c := proto.Clone(e).(*asterismv1.Event)
		c.Audit = nil
		if c.TsMs > 0 {
			c.TsMs += skewAdjMs
		}
		c.EntityId = v.hs.qualify(orDefault(c.EntityId, "host"))
		c.Message = dockercli.Truncate(dockercli.RedactText(c.Message), 1000)
		c.Kind = dockercli.Truncate(orDefault(c.Kind, "agent.event"), 64)
		if strings.HasPrefix(c.Kind, "audit.") {
			c.Kind = "agent." + c.Kind // never let an agent forge hub audit records
		}
		c.Id = "agent:" + host + ":" + dockercli.Truncate(orDefault(c.Id, fmt.Sprintf("%d-%d", c.TsMs, i)), 120)
		out = append(out, c)
	}
	v.mu.Lock()
	for _, e := range out {
		v.events = append(v.events, proto.Clone(e).(*asterismv1.Event))
	}
	if len(v.events) > maxViewEvents {
		v.events = append([]*asterismv1.Event(nil), v.events[len(v.events)-maxViewEvents:]...)
	}
	v.mu.Unlock()
	return out
}

// applyContainerUpdate mirrors an action result onto the cached container
// and workload until the agent's inventory delta arrives.
func (v *HostView) applyContainerUpdate(c *asterismv1.Container) {
	if c == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for i, cur := range v.containers {
		if cur.ContainerId == c.ContainerId || cur.Id == c.Id {
			upd := proto.Clone(c).(*asterismv1.Container)
			upd.Role, upd.ProjectId = cur.Role, cur.ProjectId
			if upd.Id == "" {
				upd.Id = cur.Id
			}
			v.containers[i] = upd
			if w, ok := v.out.Workloads[upd.Id]; ok {
				w.Health, w.State = assemble.ParseDockerStatus(dockercli.HumanStatus(upd, time.Now()))
				w.StartedAtMs, w.RestartCount = upd.StartedAtMs, upd.RestartCount
			}
		}
	}
}

// ---------------------------------------------------------------------------
// service.HostProvider
// ---------------------------------------------------------------------------

// HostSnapshot returns static host facts merged with live vitals.
func (v *HostView) HostSnapshot() *asterismv1.HostInfo {
	v.mu.RLock()
	defer v.mu.RUnlock()
	h := &asterismv1.HostInfo{}
	switch {
	case v.host != nil:
		h = proto.Clone(v.host).(*asterismv1.HostInfo)
	case v.hello != nil:
		h = proto.Clone(v.hello).(*asterismv1.HostInfo)
	}
	h.Id = "host"
	h.SourceMode = "agent"
	h.SshTarget = ""
	if h.Hostname == "" {
		h.Hostname = v.hs.agent.Name
	}
	if le := v.live["host"]; le != nil {
		get := func(k string) (float64, bool) { x, ok := le.vals[k]; return x, ok }
		if x, ok := get("cpu.util"); ok {
			h.CpuUtil = x
		}
		if x, ok := get("cpu.steal"); ok {
			h.CpuStealUtil = x
		}
		if x, ok := get("mem.used_bytes"); ok {
			h.MemUsedBytes = uint64(x)
		}
		if x, ok := get("mem.total_bytes"); ok && x > 0 {
			h.MemTotalBytes = uint64(x)
		}
		if x, ok := get("swap.used_bytes"); ok {
			h.SwapUsedBytes = uint64(x)
		}
		if x, ok := get("swap.total_bytes"); ok && x > 0 {
			h.SwapTotalBytes = uint64(x)
		}
		if x, ok := get("load.1"); ok {
			h.Load_1 = x
		}
		if x, ok := get("load.5"); ok {
			h.Load_5 = x
		}
		if x, ok := get("load.15"); ok {
			h.Load_15 = x
		}
		if x, ok := get("net.rx_bps"); ok {
			h.NetRxBps = x
		}
		if x, ok := get("net.tx_bps"); ok {
			h.NetTxBps = x
		}
		if x, ok := get("uptime.seconds"); ok {
			h.UptimeSeconds = uint64(x)
		}
		if x, ok := get("disk.used_bytes"); ok {
			h.DiskUsedBytes = uint64(x)
		}
		if x, ok := get("disk.total_bytes"); ok && x > 0 {
			h.DiskTotalBytes = uint64(x)
		}
	}
	for _, fs := range v.filesystems {
		if fs.GetMount() == "/" {
			h.DiskTotalBytes, h.DiskUsedBytes = fs.GetTotalBytes(), fs.GetUsedBytes()
		}
	}
	// The agent streams the first filesystem's usage as disk.used_ratio and
	// re-sends FilesystemUsage only on a 0.5% change, so the ratio is the
	// fresher value.
	if le := v.live["host"]; le != nil && h.DiskTotalBytes > 0 {
		if r, ok := le.vals["disk.used_ratio"]; ok && r >= 0 && r <= 1 {
			h.DiskUsedBytes = uint64(r * float64(h.DiskTotalBytes))
		}
	}
	if h.UptimeSeconds == 0 && h.BootTimeMs > 0 {
		if up := time.Now().UnixMilli() - h.BootTimeMs; up > 0 {
			h.UptimeSeconds = uint64(up / 1000)
		}
	}
	return h
}

// ---------------------------------------------------------------------------
// service.InventoryProvider
// ---------------------------------------------------------------------------

// Projects returns the assembled projects.
func (v *HostView) Projects() []*asterismv1.Project {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return nonNil(cloneList(v.out.Projects))
}

// Workloads returns every workload, project by project.
func (v *HostView) Workloads() []*asterismv1.Workload {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := []*asterismv1.Workload{}
	for _, p := range v.out.Projects {
		for _, w := range p.Workloads {
			out = append(out, proto.Clone(w).(*asterismv1.Workload))
		}
	}
	return out
}

// Workload returns one workload by entity id.
func (v *HostView) Workload(id string) (*asterismv1.Workload, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	w, ok := v.out.Workloads[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(w).(*asterismv1.Workload), true
}

// Ports returns every port binding.
func (v *HostView) Ports() []*asterismv1.PortBinding {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return nonNil(cloneList(v.out.Ports))
}

// IngressRoutes returns the assembled routes.
func (v *HostView) IngressRoutes() []*asterismv1.IngressRoute {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return nonNil(cloneList(v.out.Ingress))
}

// Edges returns topology edges.
func (v *HostView) Edges() []*asterismv1.TopologyEdge {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return nonNil(cloneList(v.out.Edges))
}

// Events returns inventory-derived events, agent events, and (on the primary
// view) every agent's lifecycle events.
func (v *HostView) Events() []*asterismv1.Event {
	v.mu.RLock()
	out := cloneList(v.out.Events)
	out = append(out, cloneList(v.events)...)
	v.mu.RUnlock()
	if v.hs.primary {
		out = append(out, v.hs.reg.lifecycleEvents()...)
	}
	return nonNil(out)
}

// Logs asks the agent for the last 200 lines of a workload's logs and waits
// up to LogTimeout for them.
func (v *HostView) Logs(workloadID string) []*asterismv1.LogLine {
	return v.hs.reg.fetchLogs(v.hs, workloadID)
}

// ---------------------------------------------------------------------------
// service.DockerProvider
// ---------------------------------------------------------------------------

// DockerSnapshot returns containers with live stats plus engine resources.
func (v *HostView) DockerSnapshot() dockercli.Snapshot {
	now := time.Now()
	v.mu.RLock()
	defer v.mu.RUnlock()
	snap := dockercli.Snapshot{CollectedAtMs: v.syncedMs}
	switch {
	case v.engine != nil:
		snap.Engine = proto.Clone(v.engine).(*asterismv1.DockerEngine)
	case v.syncedMs == 0:
		snap.Engine = &asterismv1.DockerEngine{Available: false, UnavailableReason: fmt.Sprintf("waiting for the first inventory from agent %s", v.hs.agent.Name)}
	default:
		snap.Engine = &asterismv1.DockerEngine{Available: len(v.containers) > 0}
	}
	for _, c := range v.containers {
		cl := proto.Clone(c).(*asterismv1.Container)
		running := cl.State == asterismv1.ContainerState_CONTAINER_STATE_RUNNING || cl.State == asterismv1.ContainerState_CONTAINER_STATE_PAUSED
		cl.Stats = nil
		if le := v.live[cl.Id]; le != nil && running {
			cl.Stats = containerStats(le, cl)
		}
		cl.UptimeSeconds = 0
		if running && cl.StartedAtMs > 0 && now.UnixMilli() > cl.StartedAtMs {
			cl.UptimeSeconds = uint64((now.UnixMilli() - cl.StartedAtMs) / 1000)
		}
		cl.Status = dockercli.HumanStatus(cl, now)
		snap.Containers = append(snap.Containers, cl)
	}
	snap.Images = cloneList(v.images)
	snap.Volumes = cloneList(v.volumes)
	snap.Networks = cloneList(v.networks)
	snap.DiskUsage = cloneMsg(v.diskUsage)
	dockercli.LinkUsage(snap.Containers, snap.Images, snap.Volumes)
	return snap
}

func containerStats(le *liveEntity, c *asterismv1.Container) *asterismv1.ContainerStats {
	val := func(keys ...string) float64 {
		for _, k := range keys {
			if x, ok := le.vals[k]; ok {
				return x
			}
		}
		return 0
	}
	st := &asterismv1.ContainerStats{
		TsMs:          le.tsMs,
		CpuUtil:       val("cpu.util"),
		MemUsedBytes:  uint64(val("mem.used_bytes")),
		MemLimitBytes: uint64(val("mem.limit_bytes")),
		NetRxBps:      val("net.rx_bps"),
		NetTxBps:      val("net.tx_bps"),
		BlkReadBps:    val("blk.read_bps", "io.read_bps"),
		BlkWriteBps:   val("blk.write_bps", "io.write_bps"),
		NetRxBytes:    uint64(val("net.rx_bytes")),
		NetTxBytes:    uint64(val("net.tx_bytes")),
		BlkReadBytes:  uint64(val("blk.read_bytes", "io.read_bytes")),
		BlkWriteBytes: uint64(val("blk.write_bytes", "io.write_bytes")),
		Pids:          uint32(val("pids.count")),
	}
	if st.MemLimitBytes == 0 && c.Resources != nil {
		st.MemLimitBytes = c.Resources.MemLimitBytes
	}
	if st.MemLimitBytes > 0 {
		st.MemUtil = float64(st.MemUsedBytes) / float64(st.MemLimitBytes)
	}
	return st
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func cloneList[T proto.Message](in []T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	for i, m := range in {
		out[i] = proto.Clone(m).(T)
	}
	return out
}

func cloneMsg[T proto.Message](m T) T {
	var zero T
	if any(m) == any(zero) || !m.ProtoReflect().IsValid() {
		return zero
	}
	return proto.Clone(m).(T)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// DirIcon implements service.DirIconSource with the agent's directory scan.
func (v *HostView) DirIcon(workingDir string) (favicon.DirRef, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	ic, ok := v.icons[workingDir]
	if !ok {
		return favicon.DirRef{}, false
	}
	return favicon.DirRef{WorkingDir: workingDir, RelPath: ic.GetRelPath(), SHA256: ic.GetSha256(), Size: int(ic.GetSizeBytes())}, true
}

// ReadDirIcon fetches the icon bytes from the agent (IconRequest).
func (v *HostView) ReadDirIcon(ctx context.Context, ref favicon.DirRef) ([]byte, error) {
	return v.hs.reg.fetchIcon(ctx, v.hs, ref.SHA256)
}
