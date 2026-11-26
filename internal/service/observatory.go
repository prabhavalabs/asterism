package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/prabhavalabs/asterism/internal/auth"
	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/store/eventlog"
	"github.com/prabhavalabs/asterism/internal/store/ring"
	"google.golang.org/protobuf/proto"
)

// HostProvider supplies live host metadata and vitals.
type HostProvider interface {
	HostSnapshot() *asterismv1.HostInfo
}

// InventoryProvider supplies projects, workloads, ports, ingress routes, topology edges, events, and logs.
type InventoryProvider interface {
	Projects() []*asterismv1.Project
	Workloads() []*asterismv1.Workload
	Workload(id string) (*asterismv1.Workload, bool)
	Ports() []*asterismv1.PortBinding
	IngressRoutes() []*asterismv1.IngressRoute
	Edges() []*asterismv1.TopologyEdge
	Events() []*asterismv1.Event
	Logs(workloadID string) []*asterismv1.LogLine
}

// Observatory is the domain service layer shared by ConnectRPC, MCP, and CLI adapters.
type Observatory struct {
	bus       *bus.Bus
	ring      *ring.Store
	host      HostProvider
	inventory InventoryProvider
	mode      string
	logger    *slog.Logger

	docker   DockerProvider
	actuator ContainerActuator
	policy   ActionPolicy
	locks    actionLocks
	// events holds service-generated lifecycle and audit events.
	events *eventlog.Log
	// display holds configured project icons keyed by project name.
	display map[string]ProjectDisplay
	// icons stores uploaded and discovered project images (nil = off).
	icons *iconManager

	// liveViewers counts open LiveService subscriptions; onViewers is told
	// whenever it changes (agent mode turns it into sampling hints).
	liveViewers atomic.Int64
	viewersMu   sync.Mutex
	onViewers   func(n int)

	agents AgentDirectory

	// alerts is nil when [alerts] enabled = false.
	alerts *alertsState
}

// AgentDirectory reports enrolled asterism-agents (agent mode).
type AgentDirectory interface {
	ListAgents() *asterismv1.ListAgentsResponse
}

// ConfigureAgents attaches the agent directory served by ListAgents.
func (o *Observatory) ConfigureAgents(d AgentDirectory) { o.agents = d }

// ListAgents returns enrolled agents and their connection state; empty
// outside agent mode.
func (o *Observatory) ListAgents() *asterismv1.ListAgentsResponse {
	if o.agents == nil {
		return &asterismv1.ListAgentsResponse{Agents: []*asterismv1.AgentStatus{}}
	}
	return o.agents.ListAgents()
}

func New(b *bus.Bus, r *ring.Store, h HostProvider, inv InventoryProvider, mode string) *Observatory {
	return &Observatory{
		bus:       b,
		ring:      r,
		host:      h,
		inventory: inv,
		mode:      mode,
		logger:    defaultLogger(),
		events:    eventlog.New(2000),
	}
}

// ConfigureProjects sets per-project display settings keyed by project name.
func (o *Observatory) ConfigureProjects(display map[string]ProjectDisplay) {
	o.display = make(map[string]ProjectDisplay, len(display))
	for name, d := range display {
		o.display[name] = d
	}
}

// SetLogger sets the logger used for audit records.
func (o *Observatory) SetLogger(l *slog.Logger) {
	if l != nil {
		o.logger = l
	}
}

// RunIngestor consumes collector messages from the bus and writes them into the hot ring store.
func (o *Observatory) RunIngestor(ctx context.Context) error {
	ch, unsub := o.bus.Subscribe()
	defer unsub()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg := <-ch:
			for _, s := range msg.Samples {
				o.ring.Append(s.EntityID, s.Metric, s.TSMs, s.Value)
			}
		}
	}
}

// SubscribeBus subscribes a live viewer (LiveService) to the bus. Every
// subscription counts as one live viewer until its unsubscribe runs.
func (o *Observatory) SubscribeBus() (<-chan bus.Message, func()) {
	ch, unsub := o.bus.Subscribe()
	o.notifyViewers(o.liveViewers.Add(1))
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			unsub()
			o.notifyViewers(o.liveViewers.Add(-1))
		})
	}
}

// LiveViewers returns the number of open live subscriptions.
func (o *Observatory) LiveViewers() int { return int(o.liveViewers.Load()) }

// OnLiveViewers registers a callback for live-viewer count changes. It is
// called synchronously with the new count and must not block.
func (o *Observatory) OnLiveViewers(fn func(n int)) {
	o.viewersMu.Lock()
	o.onViewers = fn
	o.viewersMu.Unlock()
}

func (o *Observatory) notifyViewers(n int64) {
	o.viewersMu.Lock()
	fn := o.onViewers
	o.viewersMu.Unlock()
	if fn != nil {
		fn(int(n))
	}
}

func (o *Observatory) Overview(p auth.Principal) *asterismv1.GetOverviewResponse {
	var hostInfo *asterismv1.HostInfo
	if o.host != nil {
		hostInfo = o.host.HostSnapshot()
	}
	projects := o.ListProjects("")
	ports := o.ListPorts(asterismv1.PortExposure_PORT_EXPOSURE_UNSPECIFIED)
	ingress := o.ListIngress()
	events := o.ListEvents("", 12)

	var publicPorts uint32
	for _, p := range ports {
		if p.Exposure == asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC {
			publicPorts++
		}
	}
	activeAlerts := o.activeAlertCount()
	if firing, ok := o.FiringAlertCount(); ok {
		activeAlerts = firing
	}

	var counts *asterismv1.ContainerCounts
	if o.docker != nil {
		counts = dockercli.Counts(o.docker.DockerSnapshot().Containers)
	}

	return &asterismv1.GetOverviewResponse{
		ContainerCounts:   counts,
		Host:              hostInfo,
		Projects:          projects,
		PublicPortCount:   publicPorts,
		IngressRouteCount: uint32(len(ingress)),
		ActiveAlertCount:  activeAlerts,
		RecentEvents:      events,
		Capabilities:      o.Capabilities(p),
	}
}

// activeAlertCount counts warning and critical events over the whole
// retained event log (collector events plus the service log), using the same
// "alerts" rule as ListEvents(kind_group = "alerts"): audit records and
// container lifecycle events are not alerts.
func (o *Observatory) activeAlertCount() uint32 {
	var n uint32
	for _, e := range o.allEvents("") {
		if kindGroup(e.Kind) != "other" {
			continue
		}
		if b := severityBucket(e.Severity); b == "warning" || b == "critical" {
			n++
		}
	}
	return n
}

// ListProjects returns caller-owned copies of the projects in a category
// ("" or "all" for every project), each with its resolved icon and with
// workload technologies detected.
func (o *Observatory) ListProjects(category string) []*asterismv1.Project {
	if o.inventory == nil {
		return nil
	}
	cat := strings.ToLower(strings.TrimSpace(category))
	all := o.inventory.Projects()
	out := make([]*asterismv1.Project, 0, len(all))
	var byProject map[string][]iconSource
	for _, proj := range all {
		if cat != "" && cat != "all" && !strings.EqualFold(proj.Category, cat) {
			continue
		}
		if byProject == nil {
			byProject = o.containerIconSources()
		}
		out = append(out, o.decorateProject(proj, byProject[proj.Id]))
	}
	return out
}

// containerIconSources groups Docker containers (any state) by project id so
// labels on stopped or non-topology containers also count.
func (o *Observatory) containerIconSources() map[string][]iconSource {
	out := map[string][]iconSource{}
	if o.docker == nil {
		return out
	}
	for _, c := range o.docker.DockerSnapshot().Containers {
		var mem uint64
		if c.Stats != nil {
			mem = c.Stats.MemUsedBytes
		}
		out[c.ProjectId] = append(out[c.ProjectId], iconSource{id: c.Id, image: c.Image, labels: c.Labels, mem: mem})
	}
	return out
}

// decorateProject clones proj and fills service-derived fields.
func (o *Observatory) decorateProject(proj *asterismv1.Project, containers []iconSource) *asterismv1.Project {
	cp := proto.Clone(proj).(*asterismv1.Project)
	seen := make(map[string]bool, len(cp.Workloads))
	sources := make([]iconSource, 0, len(cp.Workloads)+len(containers))
	for _, w := range cp.Workloads {
		w.Tech = DetectTech(w.Image)
		seen[w.Id] = true
		sources = append(sources, iconSource{id: w.Id, image: w.Image, labels: w.Labels, mem: w.MemUsedBytes})
	}
	for _, c := range containers {
		if !seen[c.id] {
			sources = append(sources, c)
		}
	}
	// Hub config wins; a project rule from the agent (carried as project
	// labels by internal/collect/assemble) fills what the config leaves out.
	d := o.display[cp.Name]
	if d.Icon == "" {
		d.Icon = cp.Labels[LabelIcon]
	}
	if d.Color == "" {
		d.Color = cp.Labels[LabelColor]
	}
	cp.Icon = ResolveProjectIcon(cp.Name, d, sources)
	o.decorateIcon(cp)
	return cp
}

// withTech returns a copy of w with its technology detected.
func withTech(w *asterismv1.Workload) *asterismv1.Workload {
	cp := proto.Clone(w).(*asterismv1.Workload)
	cp.Tech = DetectTech(cp.Image)
	return cp
}

func (o *Observatory) GetProject(id string) (*asterismv1.Project, error) {
	for _, proj := range o.ListProjects("") {
		if proj.Id == id || proj.Name == id {
			return proj, nil
		}
	}
	return nil, fmt.Errorf("unknown project %q", id)
}

func (o *Observatory) ListWorkloads(projectID string, role asterismv1.WorkloadRole) []*asterismv1.Workload {
	if o.inventory == nil {
		return nil
	}
	all := o.inventory.Workloads()
	out := make([]*asterismv1.Workload, 0, len(all))
	for _, w := range all {
		if projectID != "" && w.ProjectId != projectID {
			continue
		}
		if role != asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED && w.Role != role {
			continue
		}
		out = append(out, withTech(w))
	}
	return out
}

func (o *Observatory) GetWorkload(id string) (*asterismv1.GetWorkloadResponse, error) {
	if o.inventory == nil {
		return nil, fmt.Errorf("unknown workload %q", id)
	}
	w, ok := o.inventory.Workload(id)
	if !ok {
		return nil, fmt.Errorf("unknown workload %q", id)
	}

	var relIngress []*asterismv1.IngressRoute
	for _, r := range o.inventory.IngressRoutes() {
		if r.ProxyWorkloadId == id {
			relIngress = append(relIngress, r)
			continue
		}
		for _, u := range r.Upstreams {
			if u.WorkloadId == id {
				relIngress = append(relIngress, r)
				break
			}
		}
	}

	var relEdges []*asterismv1.TopologyEdge
	for _, e := range o.inventory.Edges() {
		if e.SourceId == id || e.TargetId == id {
			relEdges = append(relEdges, e)
		}
	}

	return &asterismv1.GetWorkloadResponse{
		Workload:      withTech(w),
		IngressRoutes: relIngress,
		Edges:         relEdges,
		RecentEvents:  o.ListEvents(id, 10),
	}, nil
}

func (o *Observatory) ListPorts(exposure asterismv1.PortExposure) []*asterismv1.PortBinding {
	if o.inventory == nil {
		return nil
	}
	all := o.inventory.Ports()
	if exposure == asterismv1.PortExposure_PORT_EXPOSURE_UNSPECIFIED {
		return all
	}
	out := make([]*asterismv1.PortBinding, 0, len(all))
	for _, p := range all {
		if p.Exposure == exposure {
			out = append(out, p)
		}
	}
	return out
}

func (o *Observatory) ListIngress() []*asterismv1.IngressRoute {
	if o.inventory == nil {
		return nil
	}
	return o.inventory.IngressRoutes()
}

func (o *Observatory) Graph(category string) *asterismv1.Graph {
	projects := o.ListProjects(category)
	if len(projects) == 0 {
		return &asterismv1.Graph{}
	}

	ingress := o.ListIngress()
	hostnamesByWorkload := make(map[string][]string)
	proxyKindByWorkload := make(map[string]asterismv1.ProxyKind)
	strategyByWorkload := make(map[string]string)
	for _, r := range ingress {
		hostnamesByWorkload[r.ProxyWorkloadId] = append(hostnamesByWorkload[r.ProxyWorkloadId], r.Hostnames...)
		proxyKindByWorkload[r.ProxyWorkloadId] = r.ProxyKind
		if strategyByWorkload[r.ProxyWorkloadId] == "" {
			strategyByWorkload[r.ProxyWorkloadId] = r.Strategy
		}
	}

	nodes := make([]*asterismv1.TopologyNode, 0, 64)
	nodeIDs := make(map[string]bool)
	for _, proj := range projects {
		for _, w := range proj.Workloads {
			nodeIDs[w.Id] = true
			nodes = append(nodes, &asterismv1.TopologyNode{
				Id:            w.Id,
				Label:         w.Name,
				Subtitle:      w.Image,
				ProjectId:     proj.Id,
				Category:      proj.Category,
				Role:          w.Role,
				Kind:          w.Kind,
				Status:        w.Health,
				CpuUtil:       w.CpuUtil,
				MemUsedBytes:  w.MemUsedBytes,
				MemLimitBytes: w.MemLimitBytes,
				Replicas:      w.Replicas,
				Ports:         w.Ports,
				Hostnames:     hostnamesByWorkload[w.Id],
				ProxyKind:     proxyKindByWorkload[w.Id],
				Strategy:      strategyByWorkload[w.Id],
			})
		}
	}

	var edges []*asterismv1.TopologyEdge
	if o.inventory != nil {
		for _, e := range o.inventory.Edges() {
			if nodeIDs[e.SourceId] && nodeIDs[e.TargetId] {
				edges = append(edges, e)
			}
		}
	}

	return &asterismv1.Graph{
		Nodes:         nodes,
		Edges:         edges,
		Projects:      projects,
		IngressRoutes: ingress,
	}
}

func (o *Observatory) QueryRange(entityID string, metrics []string, fromMs, toMs int64) []*asterismv1.MetricSeries {
	out := make([]*asterismv1.MetricSeries, 0, len(metrics))
	for _, m := range metrics {
		pts := o.ring.Query(entityID, m, fromMs, toMs)
		protoPts := make([]*asterismv1.MetricPoint, len(pts))
		for i, pt := range pts {
			protoPts[i] = &asterismv1.MetricPoint{TsMs: pt.TSMs, Value: pt.Value}
		}
		out = append(out, &asterismv1.MetricSeries{
			EntityId: entityID,
			Metric:   m,
			Points:   protoPts,
		})
	}
	return out
}

func (o *Observatory) TopN(metric string, limit uint32) []*asterismv1.TopNItem {
	workloads := o.ListWorkloads("", asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED)
	items := make([]*asterismv1.TopNItem, 0, len(workloads))
	for _, w := range workloads {
		val := w.CpuUtil
		if metric == "mem.used_bytes" {
			val = float64(w.MemUsedBytes)
		}
		items = append(items, &asterismv1.TopNItem{
			EntityId:  w.Id,
			Name:      w.Name,
			ProjectId: w.ProjectId,
			Value:     val,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Value > items[j].Value })
	if limit > 0 && int(limit) < len(items) {
		items = items[:limit]
	}
	return items
}

// ListEvents merges collector events with service lifecycle and audit events, newest first.
func (o *Observatory) ListEvents(entityID string, limit uint32) []*asterismv1.Event {
	var all []*asterismv1.Event
	if o.inventory != nil {
		all = o.inventory.Events()
	}
	own := o.events.List(entityID, int(limit))
	out := make([]*asterismv1.Event, 0, len(all)+len(own))
	for _, e := range all {
		if entityID != "" && e.EntityId != entityID {
			continue
		}
		out = append(out, e)
	}
	out = append(out, own...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].TsMs > out[j].TsMs })
	if limit > 0 && uint32(len(out)) > limit {
		out = out[:limit]
	}
	return out
}
