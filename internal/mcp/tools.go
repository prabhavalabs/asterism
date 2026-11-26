package mcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

const (
	minPageSize     = 10
	defaultPageSize = 25
	maxPageSize     = 100
)

func pageReq(page, size int32) *asterismv1.PageRequest {
	if size <= 0 {
		size = defaultPageSize
	}
	if size < minPageSize {
		size = minPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	if page <= 0 {
		page = 1
	}
	return &asterismv1.PageRequest{Page: page, PageSize: size}
}

func call[Req, Res any](ctx context.Context, fn func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), req *Req) (*Res, error) {
	resp, err := fn(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (s *server) register(srv *sdk.Server) {
	addTool(srv, "overview", "Overview",
		"Host vitals, container counts, project health, the biggest memory users and recent warnings. Call this first.",
		s.overview)
	addTool(srv, "list_projects", "List projects",
		"Projects (compose projects, systemd units, processes) with category, health, CPU and memory cost. order_by: name, category, memory, cpu, workloads, health (default \"memory desc\").",
		s.listProjects)
	addTool(srv, "get_project", "Get project",
		"One project with all its workloads (role, state, health, CPU, memory). Accepts an id (proj:<name>) or a name.",
		s.getProject)
	addTool(srv, "list_workloads", "List workloads",
		"Workloads (containers, systemd units, processes) filtered by project and role. order_by: name, project, role, state, cpu, memory, restarts.",
		s.listWorkloads)
	addTool(srv, "get_workload", "Get workload",
		"One workload: image, role, ports, networks, environment variable names (never values), ingress routes that target it, topology edges and recent events.",
		s.getWorkload)
	addTool(srv, "list_containers", "List containers",
		"Every Docker container, running or stopped, with state, health, restarts, OOM kills, CPU, memory and ports. order_by: name, project, state, image, cpu, memory, uptime, restarts, created (default \"project, name\").",
		s.listContainers)
	addTool(srv, "get_container", "Get container",
		"Full detail for one container: command, restart policy, limits, healthcheck, mounts, networks, labels (secret-looking ones redacted), env names and recent events. Accepts ctr:<name>, a name or a container id.",
		s.getContainer)
	addTool(srv, "list_ports", "List ports",
		"Listening host and container ports with the owning workload and exposure (public, loopback, internal). order_by: exposure, port, workload, project, proto.",
		s.listPorts)
	addTool(srv, "list_ingress", "List ingress routes",
		"Reverse-proxy routes from Nginx, Caddy, HAProxy, Traefik and Cloudflare Tunnel: hostnames, listen port, TLS, strategy and upstream health. order_by: hostname, port, proxy, upstreams.",
		s.listIngress)
	addTool(srv, "list_images", "List Docker images",
		"Docker images with size, age and the containers using them. filter: all (default), unused, dangling, in_use. order_by: size, created, repository, containers (default \"size desc\").",
		s.listImages)
	addTool(srv, "list_volumes", "List Docker volumes",
		"Docker volumes with size, owning compose project and the containers mounting them. filter: all (default), unused, in_use. order_by: name, size, created, containers, project.",
		s.listVolumes)
	addTool(srv, "list_networks", "List Docker networks",
		"Docker networks with driver, subnets and attached containers. order_by: name, driver, containers, created.",
		s.listNetworks)
	addTool(srv, "disk_usage", "Disk usage",
		"Root disk usage plus the Docker breakdown (images, containers, volumes, build cache) with reclaimable space and unused image and volume counts.",
		s.diskUsage)
	addTool(srv, "list_events", "List events",
		"Lifecycle events, restarts, OOM kills, alerts, proxy reloads and audited actions, newest first. order_by: ts, severity, kind, entity (default \"ts desc\").",
		s.listEvents)
	addTool(srv, "tail_logs", "Tail logs",
		"Recent log lines for one workload (newest last), with optional substring filter. Obvious secrets are redacted.",
		s.tailLogs)
	addTool(srv, "top", "Top workloads",
		"The workloads using the most CPU or memory right now.",
		s.top)
	addTool(srv, "query_metrics", "Query metrics",
		"Summary statistics (min, max, avg, p95, last) for metrics of one entity over the last minutes; raw points on request. Metrics: cpu.util, mem.used_bytes, mem.used_ratio, net.rx_bps, net.tx_bps, load.1 (host), blk.read_bps, blk.write_bps, pids.count.",
		s.queryMetrics)
	addTool(srv, "get_topology", "Get topology",
		"Nodes and edges of the infrastructure graph (ingress, app, database and cache links), optionally around one entity.",
		s.getTopology)
}

// ---- overview ---------------------------------------------------------------

type OverviewIn struct{}

type ContainerCountsView struct {
	Total      uint32 `json:"total"`
	Running    uint32 `json:"running"`
	Paused     uint32 `json:"paused,omitempty"`
	Restarting uint32 `json:"restarting,omitempty"`
	Exited     uint32 `json:"exited,omitempty"`
	Dead       uint32 `json:"dead,omitempty"`
	Unhealthy  uint32 `json:"unhealthy,omitempty"`
	Attention  uint32 `json:"needs_attention"`
}

type OverviewOut struct {
	Host           HostView            `json:"host"`
	Containers     ContainerCountsView `json:"containers"`
	Projects       int                 `json:"projects"`
	ByCategory     map[string]int      `json:"projects_by_category"`
	NeedsAttention []ProjectView       `json:"projects_needing_attention"`
	TopMemory      []ProjectView       `json:"top_memory_projects"`
	PublicPorts    uint32              `json:"public_ports"`
	IngressRoutes  uint32              `json:"ingress_routes"`
	ActiveAlerts   uint32              `json:"active_alerts"`
	RecentEvents   []EventView         `json:"recent_warnings"`
	ActionsEnabled bool                `json:"actions_enabled"`
	DockerOK       bool                `json:"docker_available"`
}

func (s *server) overview(ctx context.Context, _ OverviewIn) (string, OverviewOut, error) {
	r, err := call(s.ctx(ctx), s.b.GetOverview, &asterismv1.GetOverviewRequest{})
	if err != nil {
		return "", OverviewOut{}, err
	}
	out := OverviewOut{
		Host:           hostView(r.GetHost()),
		Projects:       len(r.GetProjects()),
		ByCategory:     map[string]int{},
		NeedsAttention: []ProjectView{},
		TopMemory:      []ProjectView{},
		PublicPorts:    r.GetPublicPortCount(),
		IngressRoutes:  r.GetIngressRouteCount(),
		ActiveAlerts:   r.GetActiveAlertCount(),
		RecentEvents:   []EventView{},
		ActionsEnabled: r.GetCapabilities().GetActionsEnabled(),
		DockerOK:       r.GetCapabilities().GetDockerAvailable(),
	}
	if c := r.GetContainerCounts(); c != nil {
		out.Containers = ContainerCountsView{
			Total: c.GetTotal(), Running: c.GetRunning(), Paused: c.GetPaused(), Restarting: c.GetRestarting(),
			Exited: c.GetExited(), Dead: c.GetDead(), Unhealthy: c.GetUnhealthy(), Attention: c.GetAttention(),
		}
	}
	projects := slices.Clone(r.GetProjects())
	for _, p := range projects {
		cat := p.GetCategory()
		if cat == "" {
			cat = "uncategorized"
		}
		out.ByCategory[cat]++
		if st := p.GetStatus(); st == asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED || st == asterismv1.HealthStatus_HEALTH_STATUS_DOWN {
			out.NeedsAttention = append(out.NeedsAttention, projectView(p))
		}
	}
	slices.SortFunc(projects, func(a, b *asterismv1.Project) int {
		return compareDesc(a.GetMemUsedBytes(), b.GetMemUsedBytes())
	})
	for _, p := range projects[:min(5, len(projects))] {
		out.TopMemory = append(out.TopMemory, projectView(p))
	}
	for _, e := range r.GetRecentEvents() {
		if e.GetSeverity() == "info" || len(out.RecentEvents) >= 8 {
			continue
		}
		out.RecentEvents = append(out.RecentEvents, eventView(e))
	}

	h := out.Host
	summary := fmt.Sprintf("%s: CPU %.1f%% (load %.2f), memory %s of %s (%.0f%%), disk %.0f%%, up %s. %s",
		h.Hostname, h.CPUPct, h.Load1, human(h.MemUsedBytes), human(h.MemTotalBytes), h.MemPct, h.DiskPct,
		humanDuration(h.UptimeSeconds), plural(out.Projects, "project", "projects"))
	if n := len(out.NeedsAttention); n > 0 {
		summary += fmt.Sprintf(" (%d degraded or down)", n)
	}
	summary += fmt.Sprintf(", %d/%d containers running, %d need attention. %d public ports, %d ingress routes, %d active alerts.",
		out.Containers.Running, out.Containers.Total, out.Containers.Attention, out.PublicPorts, out.IngressRoutes, out.ActiveAlerts)
	return summary, out, nil
}

func compareDesc[T int | uint32 | uint64 | float64](a, b T) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	}
	return 0
}

// ---- projects and workloads ---------------------------------------------------

type ListProjectsIn struct {
	Category string `json:"category,omitempty" jsonschema:"only this category, e.g. production, staging, internal, tools"`
	Query    string `json:"query,omitempty" jsonschema:"case-insensitive substring over names and paths"`
	OrderBy  string `json:"order_by,omitempty" jsonschema:"e.g. \"memory desc\" or \"cpu desc, name\""`
	Page     int32  `json:"page,omitempty" jsonschema:"1-based page (default 1)"`
	PageSize int32  `json:"page_size,omitempty" jsonschema:"items per page, 10-100 (default 25)"`
}

type ListProjectsOut struct {
	Projects   []ProjectView     `json:"projects"`
	Page       Page              `json:"page"`
	Categories map[string]uint32 `json:"category_counts,omitempty"`
}

func (s *server) listProjects(ctx context.Context, in ListProjectsIn) (string, ListProjectsOut, error) {
	r, err := call(s.ctx(ctx), s.b.ListProjects, &asterismv1.ListProjectsRequest{
		Category: in.Category, Query: in.Query, OrderBy: in.OrderBy, Page: pageReq(in.Page, in.PageSize),
	})
	if err != nil {
		return "", ListProjectsOut{}, err
	}
	out := ListProjectsOut{Projects: []ProjectView{}, Page: pageOf(r.GetPageInfo(), "category or query"), Categories: map[string]uint32{}}
	for _, p := range r.GetProjects() {
		out.Projects = append(out.Projects, projectView(p))
	}
	for _, f := range r.GetCategoryCounts() {
		out.Categories[f.GetValue()] = f.GetCount()
	}
	return fmt.Sprintf("%d of %d projects.", len(out.Projects), out.Page.TotalSize), out, nil
}

type IDIn struct {
	ID string `json:"id" jsonschema:"entity id (e.g. proj:shop, ctr:shop-api-1) or name"`
}

type GetProjectOut struct {
	Project   ProjectView    `json:"project"`
	Workloads []WorkloadView `json:"workloads"`
}

func (s *server) getProject(ctx context.Context, in IDIn) (string, GetProjectOut, error) {
	ctx = s.ctx(ctx)
	r, err := call(ctx, s.b.GetProject, &asterismv1.GetProjectRequest{Id: in.ID})
	if err != nil {
		return "", GetProjectOut{}, s.notFound(ctx, err, "project", in.ID)
	}
	p := r.GetProject()
	out := GetProjectOut{Project: projectView(p), Workloads: []WorkloadView{}}
	workloads := p.GetWorkloads()
	if len(workloads) == 0 {
		wr, err := call(ctx, s.b.ListWorkloads, &asterismv1.ListWorkloadsRequest{ProjectId: p.GetId(), Page: pageReq(1, maxPageSize)})
		if err == nil {
			workloads = wr.GetWorkloads()
		}
	}
	for _, w := range workloads {
		out.Workloads = append(out.Workloads, workloadView(w, false))
	}
	pv := out.Project
	return fmt.Sprintf("%s (%s, %s, health %d): %s, CPU %.1f%%, memory %s (%.1f%% of the VPS).",
		pv.Name, firstNonEmpty(pv.Category, "uncategorized"), firstNonEmpty(pv.Status, "unknown"), pv.HealthScore,
		plural(len(out.Workloads), "workload", "workloads"), pv.CPUPct, human(pv.MemUsedBytes), pv.MemSharePct), out, nil
}

type ListWorkloadsIn struct {
	ProjectID string `json:"project_id,omitempty" jsonschema:"only workloads of this project (proj:<name>)"`
	Role      string `json:"role,omitempty" jsonschema:"proxy, app, worker, database, cache, queue or storage"`
	Query     string `json:"query,omitempty" jsonschema:"case-insensitive substring over names and images"`
	OrderBy   string `json:"order_by,omitempty" jsonschema:"e.g. \"memory desc\""`
	Page      int32  `json:"page,omitempty" jsonschema:"1-based page (default 1)"`
	PageSize  int32  `json:"page_size,omitempty" jsonschema:"items per page, 10-100 (default 25)"`
}

type ListWorkloadsOut struct {
	Workloads []WorkloadView `json:"workloads"`
	Page      Page           `json:"page"`
}

func (s *server) listWorkloads(ctx context.Context, in ListWorkloadsIn) (string, ListWorkloadsOut, error) {
	role, err := enumValue(in.Role, "WORKLOAD_ROLE_", asterismv1.WorkloadRole_value)
	if err != nil {
		return "", ListWorkloadsOut{}, fmt.Errorf("role: %w", err)
	}
	r, err := call(s.ctx(ctx), s.b.ListWorkloads, &asterismv1.ListWorkloadsRequest{
		ProjectId: in.ProjectID, Role: asterismv1.WorkloadRole(role), Query: in.Query, OrderBy: in.OrderBy, Page: pageReq(in.Page, in.PageSize),
	})
	if err != nil {
		return "", ListWorkloadsOut{}, err
	}
	out := ListWorkloadsOut{Workloads: []WorkloadView{}, Page: pageOf(r.GetPageInfo(), "project_id, role or query")}
	for _, w := range r.GetWorkloads() {
		out.Workloads = append(out.Workloads, workloadView(w, false))
	}
	return fmt.Sprintf("%d of %d workloads.", len(out.Workloads), out.Page.TotalSize), out, nil
}

type EdgeView struct {
	Source  string `json:"source"`
	Target  string `json:"target"`
	Kind    string `json:"kind,omitempty"`
	Origin  string `json:"origin,omitempty"`
	Proto   string `json:"proto,omitempty"`
	Port    uint32 `json:"port,omitempty"`
	Label   string `json:"label,omitempty"`
	ConnCnt uint32 `json:"connections,omitempty"`
	Weight  uint32 `json:"weight,omitempty"`
}

func edgeView(e *asterismv1.TopologyEdge) EdgeView {
	return EdgeView{
		Source:  e.GetSourceId(),
		Target:  e.GetTargetId(),
		Kind:    enumName(e.GetEdgeKind().String(), "EDGE_KIND_"),
		Origin:  enumName(e.GetSource().String(), "EDGE_SOURCE_"),
		Proto:   e.GetProto(),
		Port:    e.GetPort(),
		Label:   e.GetLabel(),
		ConnCnt: e.GetConnCount(),
		Weight:  e.GetWeight(),
	}
}

type GetWorkloadOut struct {
	Workload     WorkloadView  `json:"workload"`
	Ingress      []IngressView `json:"ingress_routes,omitempty"`
	Edges        []EdgeView    `json:"edges,omitempty"`
	RecentEvents []EventView   `json:"recent_events,omitempty"`
}

func (s *server) getWorkload(ctx context.Context, in IDIn) (string, GetWorkloadOut, error) {
	ctx = s.ctx(ctx)
	r, err := call(ctx, s.b.GetWorkload, &asterismv1.GetWorkloadRequest{Id: in.ID})
	if err != nil {
		return "", GetWorkloadOut{}, s.notFound(ctx, err, "workload", in.ID)
	}
	out := GetWorkloadOut{Workload: workloadView(r.GetWorkload(), true)}
	for _, ir := range r.GetIngressRoutes() {
		out.Ingress = append(out.Ingress, ingressView(ir))
	}
	for _, e := range r.GetEdges() {
		out.Edges = append(out.Edges, edgeView(e))
	}
	for _, e := range r.GetRecentEvents() {
		out.RecentEvents = append(out.RecentEvents, eventView(e))
	}
	w := out.Workload
	return fmt.Sprintf("%s (%s %s in %s): %s, health %s, CPU %.1f%%, memory %s, %d restarts.",
		w.Name, firstNonEmpty(w.Role, "unclassified"), w.Kind, firstNonEmpty(w.ProjectID, "no project"),
		firstNonEmpty(w.State, "unknown state"), firstNonEmpty(w.Health, "n/a"), w.CPUPct, human(w.MemUsedBytes), w.Restarts), out, nil
}

// ---- containers ---------------------------------------------------------------

type ListContainersIn struct {
	ProjectID     string   `json:"project_id,omitempty" jsonschema:"only containers of this project (proj:<name>)"`
	States        []string `json:"states,omitempty" jsonschema:"any of created, running, paused, restarting, removing, exited, dead"`
	AttentionOnly bool     `json:"attention_only,omitempty" jsonschema:"only unhealthy, restarting, dead or OOM-killed containers"`
	Query         string   `json:"query,omitempty" jsonschema:"case-insensitive substring over names, images and ports"`
	OrderBy       string   `json:"order_by,omitempty" jsonschema:"e.g. \"memory desc\" or \"restarts desc\""`
	Page          int32    `json:"page,omitempty" jsonschema:"1-based page (default 1)"`
	PageSize      int32    `json:"page_size,omitempty" jsonschema:"items per page, 10-100 (default 25)"`
}

type ListContainersOut struct {
	Containers    []ContainerView     `json:"containers"`
	Page          Page                `json:"page"`
	Counts        ContainerCountsView `json:"counts"`
	MemUsedBytes  uint64              `json:"mem_used_bytes"`
	DockerVersion string              `json:"docker_version,omitempty"`
}

func (s *server) listContainers(ctx context.Context, in ListContainersIn) (string, ListContainersOut, error) {
	var states []asterismv1.ContainerState
	for _, st := range in.States {
		v, err := enumValue(st, "CONTAINER_STATE_", asterismv1.ContainerState_value)
		if err != nil {
			return "", ListContainersOut{}, fmt.Errorf("states: %w", err)
		}
		states = append(states, asterismv1.ContainerState(v))
	}
	r, err := call(s.ctx(ctx), s.b.ListContainers, &asterismv1.ListContainersRequest{
		ProjectId: in.ProjectID, States: states, AttentionOnly: in.AttentionOnly, Query: in.Query, OrderBy: in.OrderBy,
		Page: pageReq(in.Page, in.PageSize),
	})
	if err != nil {
		return "", ListContainersOut{}, err
	}
	c := r.GetCounts()
	out := ListContainersOut{
		Containers: []ContainerView{},
		Page:       pageOf(r.GetPageInfo(), "project_id, states, attention_only or query"),
		Counts: ContainerCountsView{
			Total: c.GetTotal(), Running: c.GetRunning(), Paused: c.GetPaused(), Restarting: c.GetRestarting(),
			Exited: c.GetExited(), Dead: c.GetDead(), Unhealthy: c.GetUnhealthy(), Attention: c.GetAttention(),
		},
		MemUsedBytes:  r.GetMemUsedBytes(),
		DockerVersion: r.GetEngine().GetServerVersion(),
	}
	for _, ct := range r.GetContainers() {
		out.Containers = append(out.Containers, containerView(ct))
	}
	return fmt.Sprintf("%d of %d matching containers (host: %d/%d running, %d need attention).",
		len(out.Containers), out.Page.TotalSize, c.GetRunning(), c.GetTotal(), c.GetAttention()), out, nil
}

func (s *server) getContainer(ctx context.Context, in IDIn) (string, ContainerDetail, error) {
	ctx = s.ctx(ctx)
	r, err := call(ctx, s.b.GetContainer, &asterismv1.GetContainerRequest{Id: in.ID})
	if err != nil {
		return "", ContainerDetail{}, s.notFound(ctx, err, "container", in.ID)
	}
	d := containerDetail(r.GetContainer(), r.GetRecentEvents())
	summary := fmt.Sprintf("%s (%s): %s", d.Name, d.Image, firstNonEmpty(d.Status, d.State))
	if d.Health != "" {
		summary += ", " + d.Health
	}
	summary += fmt.Sprintf(", CPU %.1f%%, memory %s, %d restarts", d.CPUPct, human(d.MemUsedBytes), d.Restarts)
	if d.OOMKilled {
		summary += ", OOM-killed"
	}
	return summary + ".", d, nil
}

// ---- network --------------------------------------------------------------------

type ListPortsIn struct {
	Exposure string `json:"exposure,omitempty" jsonschema:"public, loopback or internal"`
	Query    string `json:"query,omitempty" jsonschema:"case-insensitive substring over workloads, processes and ports"`
	OrderBy  string `json:"order_by,omitempty" jsonschema:"e.g. \"port\" or \"exposure, port\""`
	Page     int32  `json:"page,omitempty" jsonschema:"1-based page (default 1)"`
	PageSize int32  `json:"page_size,omitempty" jsonschema:"items per page, 10-100 (default 25)"`
}

type ListPortsOut struct {
	Ports    []PortView        `json:"ports"`
	Page     Page              `json:"page"`
	Exposure map[string]uint32 `json:"exposure_counts"`
}

func (s *server) listPorts(ctx context.Context, in ListPortsIn) (string, ListPortsOut, error) {
	exp, err := enumValue(in.Exposure, "PORT_EXPOSURE_", asterismv1.PortExposure_value)
	if err != nil {
		return "", ListPortsOut{}, fmt.Errorf("exposure: %w", err)
	}
	r, err := call(s.ctx(ctx), s.b.ListPorts, &asterismv1.ListPortsRequest{
		Exposure: asterismv1.PortExposure(exp), Query: in.Query, OrderBy: in.OrderBy, Page: pageReq(in.Page, in.PageSize),
	})
	if err != nil {
		return "", ListPortsOut{}, err
	}
	ec := r.GetExposureCounts()
	out := ListPortsOut{
		Ports: []PortView{},
		Page:  pageOf(r.GetPageInfo(), "exposure or query"),
		Exposure: map[string]uint32{
			"public": ec.GetPublic(), "loopback": ec.GetLoopback(), "internal": ec.GetInternal(),
		},
	}
	for _, p := range r.GetPorts() {
		out.Ports = append(out.Ports, portView(p))
	}
	return fmt.Sprintf("%d of %d ports (%d public, %d loopback, %d internal).",
		len(out.Ports), out.Page.TotalSize, ec.GetPublic(), ec.GetLoopback(), ec.GetInternal()), out, nil
}

type ListIngressIn struct {
	Query    string `json:"query,omitempty" jsonschema:"case-insensitive substring over hostnames, proxies and upstreams"`
	OrderBy  string `json:"order_by,omitempty" jsonschema:"e.g. \"hostname\""`
	Page     int32  `json:"page,omitempty" jsonschema:"1-based page (default 1)"`
	PageSize int32  `json:"page_size,omitempty" jsonschema:"items per page, 10-100 (default 25)"`
}

type ListIngressOut struct {
	Routes []IngressView `json:"routes"`
	Page   Page          `json:"page"`
}

func (s *server) listIngress(ctx context.Context, in ListIngressIn) (string, ListIngressOut, error) {
	r, err := call(s.ctx(ctx), s.b.ListIngress, &asterismv1.ListIngressRequest{
		Query: in.Query, OrderBy: in.OrderBy, Page: pageReq(in.Page, in.PageSize),
	})
	if err != nil {
		return "", ListIngressOut{}, err
	}
	out := ListIngressOut{Routes: []IngressView{}, Page: pageOf(r.GetPageInfo(), "query")}
	down := 0
	for _, ir := range r.GetRoutes() {
		v := ingressView(ir)
		if v.UpstreamsUp < len(v.Upstreams) {
			down++
		}
		out.Routes = append(out.Routes, v)
	}
	summary := fmt.Sprintf("%d of %d ingress routes", len(out.Routes), out.Page.TotalSize)
	if down > 0 {
		summary += fmt.Sprintf("; %d on this page have unhealthy upstreams", down)
	}
	return summary + ".", out, nil
}

// ---- docker resources -------------------------------------------------------------

type ImageView struct {
	ID         string   `json:"id"`
	Tags       []string `json:"tags,omitempty"`
	SizeBytes  uint64   `json:"size_bytes"`
	Created    string   `json:"created,omitempty"`
	Dangling   bool     `json:"dangling,omitempty"`
	Containers uint32   `json:"containers"`
	UsedBy     []string `json:"used_by,omitempty"`
}

type ListImagesIn struct {
	Filter   string `json:"filter,omitempty" jsonschema:"all (default), unused, dangling or in_use"`
	Query    string `json:"query,omitempty" jsonschema:"case-insensitive substring over tags, ids and users"`
	OrderBy  string `json:"order_by,omitempty" jsonschema:"e.g. \"size desc\" or \"created\""`
	Page     int32  `json:"page,omitempty" jsonschema:"1-based page (default 1)"`
	PageSize int32  `json:"page_size,omitempty" jsonschema:"items per page, 10-100 (default 25)"`
}

type ResourceCounts struct {
	Total    uint32 `json:"total"`
	InUse    uint32 `json:"in_use"`
	Unused   uint32 `json:"unused"`
	Dangling uint32 `json:"dangling,omitempty"`
}

type ListImagesOut struct {
	Images         []ImageView    `json:"images"`
	Page           Page           `json:"page"`
	Counts         ResourceCounts `json:"counts"`
	TotalSizeBytes uint64         `json:"matching_size_bytes"`
}

func (s *server) listImages(ctx context.Context, in ListImagesIn) (string, ListImagesOut, error) {
	req := &asterismv1.ListImagesRequest{Query: in.Query, OrderBy: in.OrderBy, Page: pageReq(in.Page, in.PageSize)}
	switch strings.ToLower(strings.TrimSpace(in.Filter)) {
	case "", "all":
	case "unused":
		req.UnusedOnly = true
	case "dangling":
		req.DanglingOnly = true
	case "in_use", "in-use", "used":
		req.InUseOnly = true
	default:
		return "", ListImagesOut{}, fmt.Errorf("filter: unknown value %q; use all, unused, dangling or in_use", in.Filter)
	}
	r, err := call(s.ctx(ctx), s.b.ListImages, req)
	if err != nil {
		return "", ListImagesOut{}, err
	}
	c := r.GetCounts()
	out := ListImagesOut{
		Images:         []ImageView{},
		Page:           pageOf(r.GetPageInfo(), "filter or query"),
		Counts:         ResourceCounts{Total: c.GetTotal(), InUse: c.GetInUse(), Unused: c.GetUnused(), Dangling: c.GetDangling()},
		TotalSizeBytes: r.GetTotalSizeBytes(),
	}
	for _, img := range r.GetImages() {
		out.Images = append(out.Images, ImageView{
			ID: shortID(img.GetId()), Tags: img.GetRepoTags(), SizeBytes: img.GetSizeBytes(), Created: iso(img.GetCreatedAtMs()),
			Dangling: img.GetDangling(), Containers: img.GetContainers(), UsedBy: img.GetUsedBy(),
		})
	}
	return fmt.Sprintf("%d of %d matching images (%s). Host: %d images, %d in use, %d unused, %d dangling. Sizes share layers, so they do not add up to disk use.",
		len(out.Images), out.Page.TotalSize, human(out.TotalSizeBytes), c.GetTotal(), c.GetInUse(), c.GetUnused(), c.GetDangling()), out, nil
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

type VolumeView struct {
	Name           string   `json:"name"`
	Driver         string   `json:"driver,omitempty"`
	SizeBytes      int64    `json:"size_bytes"`
	Created        string   `json:"created,omitempty"`
	Containers     uint32   `json:"containers"`
	UsedBy         []string `json:"used_by,omitempty"`
	ComposeProject string   `json:"compose_project,omitempty"`
	Anonymous      bool     `json:"anonymous,omitempty"`
}

type ListVolumesIn struct {
	Filter   string `json:"filter,omitempty" jsonschema:"all (default), unused or in_use"`
	Query    string `json:"query,omitempty" jsonschema:"case-insensitive substring over names, projects and users"`
	OrderBy  string `json:"order_by,omitempty" jsonschema:"e.g. \"size desc\""`
	Page     int32  `json:"page,omitempty" jsonschema:"1-based page (default 1)"`
	PageSize int32  `json:"page_size,omitempty" jsonschema:"items per page, 10-100 (default 25)"`
}

type ListVolumesOut struct {
	Volumes        []VolumeView   `json:"volumes"`
	Page           Page           `json:"page"`
	Counts         ResourceCounts `json:"counts"`
	TotalSizeBytes uint64         `json:"matching_size_bytes"`
}

func (s *server) listVolumes(ctx context.Context, in ListVolumesIn) (string, ListVolumesOut, error) {
	req := &asterismv1.ListVolumesRequest{Query: in.Query, OrderBy: in.OrderBy, Page: pageReq(in.Page, in.PageSize)}
	switch strings.ToLower(strings.TrimSpace(in.Filter)) {
	case "", "all":
	case "unused":
		req.UnusedOnly = true
	case "in_use", "in-use", "used":
		req.InUseOnly = true
	default:
		return "", ListVolumesOut{}, fmt.Errorf("filter: unknown value %q; use all, unused or in_use", in.Filter)
	}
	r, err := call(s.ctx(ctx), s.b.ListVolumes, req)
	if err != nil {
		return "", ListVolumesOut{}, err
	}
	c := r.GetCounts()
	out := ListVolumesOut{
		Volumes:        []VolumeView{},
		Page:           pageOf(r.GetPageInfo(), "filter or query"),
		Counts:         ResourceCounts{Total: c.GetTotal(), InUse: c.GetInUse(), Unused: c.GetUnused()},
		TotalSizeBytes: r.GetTotalSizeBytes(),
	}
	for _, v := range r.GetVolumes() {
		out.Volumes = append(out.Volumes, VolumeView{
			Name: v.GetName(), Driver: v.GetDriver(), SizeBytes: v.GetSizeBytes(), Created: iso(v.GetCreatedAtMs()),
			Containers: v.GetContainers(), UsedBy: v.GetUsedBy(), ComposeProject: v.GetComposeProject(), Anonymous: v.GetAnonymous(),
		})
	}
	return fmt.Sprintf("%d of %d matching volumes (%s). Host: %d volumes, %d in use, %d unused. A size of -1 means unknown. Unused volumes can still hold data: inspect before deleting.",
		len(out.Volumes), out.Page.TotalSize, human(out.TotalSizeBytes), c.GetTotal(), c.GetInUse(), c.GetUnused()), out, nil
}

type NetworkView struct {
	Name           string   `json:"name"`
	Driver         string   `json:"driver,omitempty"`
	Scope          string   `json:"scope,omitempty"`
	Internal       bool     `json:"internal,omitempty"`
	Builtin        bool     `json:"builtin,omitempty"`
	Subnets        []string `json:"subnets,omitempty"`
	ComposeProject string   `json:"compose_project,omitempty"`
	Containers     []string `json:"containers"`
}

type ListNetworksIn struct {
	UserDefinedOnly bool   `json:"user_defined_only,omitempty" jsonschema:"hide the built-in bridge, host and none networks"`
	Query           string `json:"query,omitempty" jsonschema:"case-insensitive substring over names, projects and containers"`
	OrderBy         string `json:"order_by,omitempty" jsonschema:"e.g. \"containers desc\""`
	Page            int32  `json:"page,omitempty" jsonschema:"1-based page (default 1)"`
	PageSize        int32  `json:"page_size,omitempty" jsonschema:"items per page, 10-100 (default 25)"`
}

type ListNetworksOut struct {
	Networks []NetworkView `json:"networks"`
	Page     Page          `json:"page"`
	Unused   int           `json:"unused_on_page"`
}

func (s *server) listNetworks(ctx context.Context, in ListNetworksIn) (string, ListNetworksOut, error) {
	r, err := call(s.ctx(ctx), s.b.ListNetworks, &asterismv1.ListNetworksRequest{
		UserDefinedOnly: in.UserDefinedOnly, Query: in.Query, OrderBy: in.OrderBy, Page: pageReq(in.Page, in.PageSize),
	})
	if err != nil {
		return "", ListNetworksOut{}, err
	}
	out := ListNetworksOut{Networks: []NetworkView{}, Page: pageOf(r.GetPageInfo(), "user_defined_only or query")}
	for _, n := range r.GetNetworks() {
		v := NetworkView{
			Name: n.GetName(), Driver: n.GetDriver(), Scope: n.GetScope(), Internal: n.GetInternal(), Builtin: n.GetBuiltin(),
			Subnets: n.GetSubnets(), ComposeProject: n.GetComposeProject(), Containers: []string{},
		}
		for _, a := range n.GetContainers() {
			v.Containers = append(v.Containers, a.GetName())
		}
		if len(v.Containers) == 0 && !v.Builtin {
			out.Unused++
		}
		out.Networks = append(out.Networks, v)
	}
	return fmt.Sprintf("%d of %d networks; %d user-defined networks on this page have no running container attached.",
		len(out.Networks), out.Page.TotalSize, out.Unused), out, nil
}

type DiskCategoryView struct {
	Kind             string `json:"kind"`
	Total            uint32 `json:"total"`
	Active           uint32 `json:"active"`
	SizeBytes        uint64 `json:"size_bytes"`
	ReclaimableBytes uint64 `json:"reclaimable_bytes"`
}

type DiskUsageOut struct {
	RootDiskUsedBytes  uint64             `json:"root_disk_used_bytes"`
	RootDiskTotalBytes uint64             `json:"root_disk_total_bytes"`
	RootDiskPct        float64            `json:"root_disk_pct"`
	DockerSizeBytes    uint64             `json:"docker_size_bytes"`
	DockerReclaimable  uint64             `json:"docker_reclaimable_bytes"`
	Categories         []DiskCategoryView `json:"docker_categories"`
	Images             ResourceCounts     `json:"images"`
	Volumes            ResourceCounts     `json:"volumes"`
	DockerRootDir      string             `json:"docker_root_dir,omitempty"`
	StorageDriver      string             `json:"storage_driver,omitempty"`
	CollectedAt        string             `json:"collected_at,omitempty"`
}

func (s *server) diskUsage(ctx context.Context, _ OverviewIn) (string, DiskUsageOut, error) {
	ctx = s.ctx(ctx)
	du, err := call(ctx, s.b.GetDiskUsage, &asterismv1.GetDiskUsageRequest{})
	if err != nil {
		return "", DiskUsageOut{}, err
	}
	out := DiskUsageOut{
		DockerSizeBytes:   du.GetUsage().GetTotalSizeBytes(),
		DockerReclaimable: du.GetUsage().GetTotalReclaimableBytes(),
		Categories:        []DiskCategoryView{},
		DockerRootDir:     du.GetEngine().GetDockerRootDir(),
		StorageDriver:     du.GetEngine().GetStorageDriver(),
		CollectedAt:       iso(du.GetUsage().GetCollectedAtMs()),
	}
	for _, c := range du.GetUsage().GetCategories() {
		out.Categories = append(out.Categories, DiskCategoryView{
			Kind: c.GetKind(), Total: c.GetTotalCount(), Active: c.GetActiveCount(),
			SizeBytes: c.GetSizeBytes(), ReclaimableBytes: c.GetReclaimableBytes(),
		})
	}
	if ov, err := call(ctx, s.b.GetOverview, &asterismv1.GetOverviewRequest{}); err == nil {
		h := ov.GetHost()
		out.RootDiskUsedBytes, out.RootDiskTotalBytes = h.GetDiskUsedBytes(), h.GetDiskTotalBytes()
		out.RootDiskPct = share(out.RootDiskUsedBytes, out.RootDiskTotalBytes)
	}
	if im, err := call(ctx, s.b.ListImages, &asterismv1.ListImagesRequest{Page: pageReq(1, 1)}); err == nil {
		c := im.GetCounts()
		out.Images = ResourceCounts{Total: c.GetTotal(), InUse: c.GetInUse(), Unused: c.GetUnused(), Dangling: c.GetDangling()}
	}
	if vo, err := call(ctx, s.b.ListVolumes, &asterismv1.ListVolumesRequest{Page: pageReq(1, 1)}); err == nil {
		c := vo.GetCounts()
		out.Volumes = ResourceCounts{Total: c.GetTotal(), InUse: c.GetInUse(), Unused: c.GetUnused()}
	}

	parts := make([]string, 0, len(out.Categories))
	for _, c := range out.Categories {
		parts = append(parts, fmt.Sprintf("%s %s (%s reclaimable)", c.Kind, human(c.SizeBytes), human(c.ReclaimableBytes)))
	}
	summary := fmt.Sprintf("Root disk %s of %s (%.0f%%). Docker uses %s, %s reclaimable: %s. %d unused of %d images, %d unused of %d volumes.",
		human(out.RootDiskUsedBytes), human(out.RootDiskTotalBytes), out.RootDiskPct,
		human(out.DockerSizeBytes), human(out.DockerReclaimable), strings.Join(parts, "; "),
		out.Images.Unused, out.Images.Total, out.Volumes.Unused, out.Volumes.Total)
	return summary, out, nil
}

// ---- events and logs ------------------------------------------------------------------

type ListEventsIn struct {
	EntityID   string   `json:"entity_id,omitempty" jsonschema:"only events for this entity (e.g. ctr:shop-api-1)"`
	Severities []string `json:"severities,omitempty" jsonschema:"any of info, warning, critical"`
	Kind       string   `json:"kind,omitempty" jsonschema:"lifecycle, audit or other"`
	Query      string   `json:"query,omitempty" jsonschema:"case-insensitive substring over messages and entities"`
	OrderBy    string   `json:"order_by,omitempty" jsonschema:"default \"ts desc\""`
	Page       int32    `json:"page,omitempty" jsonschema:"1-based page (default 1)"`
	PageSize   int32    `json:"page_size,omitempty" jsonschema:"items per page, 10-100 (default 25)"`
}

type ListEventsOut struct {
	Events     []EventView       `json:"events"`
	Page       Page              `json:"page"`
	Severities map[string]uint32 `json:"severity_counts"`
}

func (s *server) listEvents(ctx context.Context, in ListEventsIn) (string, ListEventsOut, error) {
	r, err := call(s.ctx(ctx), s.b.ListEvents, &asterismv1.ListEventsRequest{
		EntityId: in.EntityID, Severities: in.Severities, KindGroup: in.Kind, Query: in.Query, OrderBy: in.OrderBy,
		Page: pageReq(in.Page, in.PageSize),
	})
	if err != nil {
		return "", ListEventsOut{}, err
	}
	sc := r.GetSeverityCounts()
	out := ListEventsOut{
		Events:     []EventView{},
		Page:       pageOf(r.GetPageInfo(), "entity_id, severities, kind or query"),
		Severities: map[string]uint32{"info": sc.GetInfo(), "warning": sc.GetWarning(), "critical": sc.GetCritical()},
	}
	for _, e := range r.GetEvents() {
		out.Events = append(out.Events, eventView(e))
	}
	return fmt.Sprintf("%d of %d events (%d critical, %d warning, %d info).",
		len(out.Events), out.Page.TotalSize, sc.GetCritical(), sc.GetWarning(), sc.GetInfo()), out, nil
}

type TailLogsIn struct {
	WorkloadID string `json:"workload_id" jsonschema:"workload or container id, e.g. ctr:shop-api-1"`
	Limit      uint32 `json:"limit,omitempty" jsonschema:"lines to return, 1-500 (default 100)"`
	Grep       string `json:"grep,omitempty" jsonschema:"only lines containing this substring (case-insensitive)"`
	Before     string `json:"before,omitempty" jsonschema:"RFC 3339 time; return lines older than this (use next_before from a previous call to page back)"`
}

type LogLineView struct {
	Time   string `json:"time"`
	Stream string `json:"stream,omitempty"`
	Text   string `json:"text"`
}

type TailLogsOut struct {
	Lines      []LogLineView `json:"lines"`
	HasMore    bool          `json:"has_more"`
	NextBefore string        `json:"next_before,omitempty"`
}

func (s *server) tailLogs(ctx context.Context, in TailLogsIn) (string, TailLogsOut, error) {
	if strings.TrimSpace(in.WorkloadID) == "" {
		return "", TailLogsOut{}, fmt.Errorf("workload_id is required; find ids with list_workloads or list_containers")
	}
	limit := in.Limit
	if limit == 0 {
		limit = 100
	}
	limit = min(limit, 500)
	var before int64
	if in.Before != "" {
		t, err := parseTime(in.Before)
		if err != nil {
			return "", TailLogsOut{}, fmt.Errorf("before: %w", err)
		}
		before = t.UnixMilli()
	}
	ctx = s.ctx(ctx)
	r, err := call(ctx, s.b.TailLogs, &asterismv1.TailLogsRequest{WorkloadId: in.WorkloadID, Limit: limit, Grep: in.Grep, BeforeTsMs: before})
	if err != nil {
		return "", TailLogsOut{}, s.notFound(ctx, err, "workload", in.WorkloadID)
	}
	out := TailLogsOut{Lines: []LogLineView{}, HasMore: r.GetHasMore(), NextBefore: iso(r.GetNextBeforeTsMs())}
	for _, l := range r.GetLines() {
		out.Lines = append(out.Lines, LogLineView{Time: iso(l.GetTsMs()), Stream: l.GetStream(), Text: truncate(l.GetText(), 2000)})
	}
	summary := fmt.Sprintf("%s from %s", plural(len(out.Lines), "log line", "log lines"), in.WorkloadID)
	if in.Grep != "" {
		summary += fmt.Sprintf(" matching %q", in.Grep)
	}
	if out.HasMore {
		summary += "; older lines exist (pass before=next_before)"
	}
	return summary + ".", out, nil
}

// ---- metrics and topology ------------------------------------------------------------

type TopIn struct {
	Metric string `json:"metric,omitempty" jsonschema:"cpu (default) or memory"`
	Limit  uint32 `json:"limit,omitempty" jsonschema:"how many workloads, 1-50 (default 10)"`
}

type TopItem struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	ProjectID string  `json:"project_id,omitempty"`
	CPUPct    float64 `json:"cpu_pct,omitempty"`
	MemBytes  uint64  `json:"mem_used_bytes,omitempty"`
}

type TopOut struct {
	Metric string    `json:"metric"`
	Items  []TopItem `json:"items"`
}

func (s *server) top(ctx context.Context, in TopIn) (string, TopOut, error) {
	metric, label := "cpu.util", "cpu"
	switch strings.ToLower(strings.TrimSpace(in.Metric)) {
	case "", "cpu", "cpu.util":
	case "memory", "mem", "mem.used_bytes":
		metric, label = "mem.used_bytes", "memory"
	default:
		return "", TopOut{}, fmt.Errorf("metric: unknown value %q; use cpu or memory", in.Metric)
	}
	limit := in.Limit
	if limit == 0 {
		limit = 10
	}
	limit = min(limit, 50)
	r, err := call(s.ctx(ctx), s.b.TopN, &asterismv1.TopNRequest{Metric: metric, Limit: limit})
	if err != nil {
		return "", TopOut{}, err
	}
	out := TopOut{Metric: label, Items: []TopItem{}}
	names := make([]string, 0, 3)
	for _, it := range r.GetItems() {
		item := TopItem{ID: it.GetEntityId(), Name: it.GetName(), ProjectID: it.GetProjectId()}
		if label == "memory" {
			item.MemBytes = uint64(it.GetValue())
			if len(names) < 3 {
				names = append(names, fmt.Sprintf("%s %s", item.Name, human(item.MemBytes)))
			}
		} else {
			item.CPUPct = pct(it.GetValue())
			if len(names) < 3 {
				names = append(names, fmt.Sprintf("%s %.1f%%", item.Name, item.CPUPct))
			}
		}
		out.Items = append(out.Items, item)
	}
	return fmt.Sprintf("Top %d workloads by %s: %s.", len(out.Items), label, strings.Join(names, ", ")), out, nil
}

type QueryMetricsIn struct {
	EntityID string   `json:"entity_id,omitempty" jsonschema:"host (default) or a workload id such as ctr:shop-api-1"`
	Metrics  []string `json:"metrics,omitempty" jsonschema:"metric names (default cpu.util and mem.used_bytes)"`
	Window   string   `json:"window,omitempty" jsonschema:"how far back, e.g. 5m or 15m (default 15m, maximum 15m)"`
	Raw      bool     `json:"raw,omitempty" jsonschema:"also return the points, downsampled to at most 500"`
}

type PointView struct {
	Time  string  `json:"t"`
	Value float64 `json:"v"`
}

type SeriesStats struct {
	Metric string      `json:"metric"`
	Unit   string      `json:"unit"`
	Count  int         `json:"count"`
	Min    float64     `json:"min"`
	Max    float64     `json:"max"`
	Avg    float64     `json:"avg"`
	P95    float64     `json:"p95"`
	Last   float64     `json:"last"`
	Points []PointView `json:"points,omitempty"`
}

type QueryMetricsOut struct {
	EntityID string        `json:"entity_id"`
	From     string        `json:"from"`
	To       string        `json:"to"`
	Series   []SeriesStats `json:"series"`
}

func (s *server) queryMetrics(ctx context.Context, in QueryMetricsIn) (string, QueryMetricsOut, error) {
	entity := firstNonEmpty(strings.TrimSpace(in.EntityID), "host")
	metrics := in.Metrics
	if len(metrics) == 0 {
		metrics = []string{"cpu.util", "mem.used_bytes"}
	}
	window, err := parseWindow(in.Window)
	if err != nil {
		return "", QueryMetricsOut{}, fmt.Errorf("window: %w", err)
	}
	to := nowFunc()
	from := to.Add(-window)
	r, err := call(s.ctx(ctx), s.b.QueryRange, &asterismv1.QueryRangeRequest{
		EntityId: entity, Metrics: metrics, FromMs: from.UnixMilli(), ToMs: to.UnixMilli(),
	})
	if err != nil {
		return "", QueryMetricsOut{}, err
	}
	out := QueryMetricsOut{EntityID: entity, From: iso(from.UnixMilli()), To: iso(to.UnixMilli()), Series: []SeriesStats{}}
	parts := []string{}
	for _, ser := range r.GetSeries() {
		st := seriesStats(ser.GetMetric(), ser.GetPoints(), in.Raw)
		out.Series = append(out.Series, st)
		if st.Count == 0 {
			parts = append(parts, st.Metric+" no data")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s avg %s, max %s, last %s", st.Metric, fmtValue(st.Avg, st.Unit), fmtValue(st.Max, st.Unit), fmtValue(st.Last, st.Unit)))
	}
	return fmt.Sprintf("%s over the last %s: %s.", entity, window, strings.Join(parts, "; ")), out, nil
}

type TopologyIn struct {
	Category string `json:"category,omitempty" jsonschema:"only projects of this category"`
	Focus    string `json:"focus,omitempty" jsonschema:"entity id to centre on; returns only nodes within depth hops"`
	Depth    int    `json:"depth,omitempty" jsonschema:"hops from focus, 1 or 2 (default 1)"`
}

type NodeView struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	ProjectID string   `json:"project_id,omitempty"`
	Role      string   `json:"role,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Status    string   `json:"status,omitempty"`
	CPUPct    float64  `json:"cpu_pct"`
	MemBytes  uint64   `json:"mem_used_bytes"`
	Replicas  uint32   `json:"replicas,omitempty"`
	Hostnames []string `json:"hostnames,omitempty"`
	Proxy     string   `json:"proxy,omitempty"`
	Strategy  string   `json:"strategy,omitempty"`
}

type TopologyOut struct {
	Nodes     []NodeView `json:"nodes"`
	Edges     []EdgeView `json:"edges"`
	Truncated bool       `json:"truncated"`
	Hint      string     `json:"hint,omitempty"`
}

const maxNodes = 150

func (s *server) getTopology(ctx context.Context, in TopologyIn) (string, TopologyOut, error) {
	r, err := call(s.ctx(ctx), s.b.GetGraph, &asterismv1.GetGraphRequest{Category: in.Category})
	if err != nil {
		return "", TopologyOut{}, err
	}
	g := r.GetGraph()
	keep := map[string]bool{}
	if focus := strings.TrimSpace(in.Focus); focus != "" {
		found := false
		for _, n := range g.GetNodes() {
			if n.GetId() == focus {
				found = true
				break
			}
		}
		if !found {
			return "", TopologyOut{}, fmt.Errorf("unknown entity %q in the topology; node ids look like ctr:<name> or unit:<name> (see list_workloads)", focus)
		}
		depth := min(max(in.Depth, 1), 2)
		keep[focus] = true
		for range depth {
			for _, e := range g.GetEdges() {
				if keep[e.GetSourceId()] || keep[e.GetTargetId()] {
					keep[e.GetSourceId()], keep[e.GetTargetId()] = true, true
				}
			}
		}
	}
	out := TopologyOut{Nodes: []NodeView{}, Edges: []EdgeView{}}
	included := map[string]bool{}
	for _, n := range g.GetNodes() {
		if len(keep) > 0 && !keep[n.GetId()] {
			continue
		}
		if len(out.Nodes) >= maxNodes {
			out.Truncated = true
			out.Hint = "graph capped at 150 nodes; pass category or focus to narrow"
			break
		}
		included[n.GetId()] = true
		out.Nodes = append(out.Nodes, NodeView{
			ID: n.GetId(), Label: n.GetLabel(), ProjectID: n.GetProjectId(), Role: roleName(n.GetRole()),
			Kind: enumName(n.GetKind().String(), "WORKLOAD_KIND_"), Status: healthName(n.GetStatus()),
			CPUPct: pct(n.GetCpuUtil()), MemBytes: n.GetMemUsedBytes(), Replicas: n.GetReplicas(),
			Hostnames: n.GetHostnames(), Proxy: enumName(n.GetProxyKind().String(), "PROXY_KIND_"), Strategy: n.GetStrategy(),
		})
	}
	for _, e := range g.GetEdges() {
		if included[e.GetSourceId()] && included[e.GetTargetId()] {
			out.Edges = append(out.Edges, edgeView(e))
		}
	}
	return fmt.Sprintf("%d nodes and %d edges.", len(out.Nodes), len(out.Edges)), out, nil
}

// ---- helpers ------------------------------------------------------------------------------

// notFound turns a NotFound error into an actionable message with up to
// three similar ids.
func (s *server) notFound(ctx context.Context, err error, kind, id string) error {
	if connect.CodeOf(err) != connect.CodeNotFound {
		return err
	}
	needle := id
	if i := strings.IndexByte(needle, ':'); i >= 0 {
		needle = needle[i+1:]
	}
	var ids []string
	switch kind {
	case "project":
		if r, e := call(ctx, s.b.ListProjects, &asterismv1.ListProjectsRequest{Query: needle, Page: pageReq(1, 3)}); e == nil {
			for _, p := range r.GetProjects() {
				ids = append(ids, p.GetId())
			}
		}
	case "container":
		if r, e := call(ctx, s.b.ListContainers, &asterismv1.ListContainersRequest{Query: needle, Page: pageReq(1, 3)}); e == nil {
			for _, c := range r.GetContainers() {
				ids = append(ids, c.GetId())
			}
		}
	default:
		if r, e := call(ctx, s.b.ListWorkloads, &asterismv1.ListWorkloadsRequest{Query: needle, Page: pageReq(1, 3)}); e == nil {
			for _, w := range r.GetWorkloads() {
				ids = append(ids, w.GetId())
			}
		}
	}
	msg := fmt.Sprintf("unknown %s %q", kind, id)
	if len(ids) > 0 {
		return fmt.Errorf("%s; did you mean %s?", msg, strings.Join(ids, ", "))
	}
	return fmt.Errorf("%s; list them with list_%ss", msg, kind)
}
