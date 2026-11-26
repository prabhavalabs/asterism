package service

import (
	"cmp"
	"slices"
	"strings"

	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// Paged List RPCs for inventory, events, and logs. Every method filters,
// searches, sorts, and pages on the server; facet counts ignore the page and
// their own filter (docs/BLUEPRINT.md §8.7).

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

func projectWorkloads(p *asterismv1.Project) uint32 {
	return max(p.WorkloadCount, uint32(len(p.Workloads)))
}

var projectSorter = newSorter("project", "memory desc",
	func(p *asterismv1.Project) string { return p.Id },
	byFold("name", func(p *asterismv1.Project) string { return p.Name }),
	byFold("category", func(p *asterismv1.Project) string { return p.Category }),
	by("memory", func(p *asterismv1.Project) uint64 { return p.MemUsedBytes }),
	by("cpu", func(p *asterismv1.Project) float64 { return p.CpuUtil }),
	by("workloads", projectWorkloads),
	by("health", func(p *asterismv1.Project) uint32 { return p.HealthScore }),
)

// ListProjectsPage serves InventoryService.ListProjects.
func (o *Observatory) ListProjectsPage(req *asterismv1.ListProjectsRequest) (*asterismv1.ListProjectsResponse, error) {
	cat := strings.ToLower(strings.TrimSpace(req.GetCategory()))
	q := normQuery(req.GetQuery())
	filters := []filter[*asterismv1.Project]{
		{facet: "category", match: func(p *asterismv1.Project) bool {
			return cat == "" || cat == "all" || strings.EqualFold(p.Category, cat)
		}},
		{match: func(p *asterismv1.Project) bool {
			return containsAny(q, p.Name, p.Id, p.Category, p.Kind, p.WorkingDir)
		}},
	}
	all := o.ListProjects("")
	items, info, err := listPage(matching(all, filters, ""), projectSorter, req.GetOrderBy(), req.GetPage())
	if err != nil {
		return nil, err
	}
	return &asterismv1.ListProjectsResponse{
		Projects:       items,
		PageInfo:       info,
		CategoryCounts: countBy(matching(all, filters, "category"), func(p *asterismv1.Project) string { return p.Category }),
	}, nil
}

// ---------------------------------------------------------------------------
// Workloads
// ---------------------------------------------------------------------------

var workloadSorter = newSorter("workload", "project, name",
	func(w *asterismv1.Workload) string { return w.Id },
	byFold("name", func(w *asterismv1.Workload) string { return w.Name }),
	by("project", func(w *asterismv1.Workload) string { return w.ProjectId }),
	by("role", func(w *asterismv1.Workload) asterismv1.WorkloadRole { return w.Role }),
	by("state", func(w *asterismv1.Workload) string { return w.State }),
	by("cpu", func(w *asterismv1.Workload) float64 { return w.CpuUtil }),
	by("memory", func(w *asterismv1.Workload) uint64 { return w.MemUsedBytes }),
	by("restarts", func(w *asterismv1.Workload) uint32 { return w.RestartCount }),
)

// ListWorkloadsPage serves InventoryService.ListWorkloads.
func (o *Observatory) ListWorkloadsPage(req *asterismv1.ListWorkloadsRequest) (*asterismv1.ListWorkloadsResponse, error) {
	q := normQuery(req.GetQuery())
	filters := []filter[*asterismv1.Workload]{
		{facet: "project", match: func(w *asterismv1.Workload) bool {
			return req.GetProjectId() == "" || w.ProjectId == req.GetProjectId()
		}},
		{facet: "role", match: func(w *asterismv1.Workload) bool {
			return req.GetRole() == asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED || w.Role == req.GetRole()
		}},
		{match: func(w *asterismv1.Workload) bool {
			return containsAny(q, w.Name, w.Id, w.Image, w.ServiceName, w.State)
		}},
	}
	all := o.ListWorkloads("", asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED)
	items, info, err := listPage(matching(all, filters, ""), workloadSorter, req.GetOrderBy(), req.GetPage())
	if err != nil {
		return nil, err
	}
	return &asterismv1.ListWorkloadsResponse{
		Workloads:     items,
		PageInfo:      info,
		RoleCounts:    roleCounts(matching(all, filters, "role")),
		ProjectCounts: countBy(matching(all, filters, "project"), func(w *asterismv1.Workload) string { return w.ProjectId }),
	}, nil
}

func roleCounts(ws []*asterismv1.Workload) []*asterismv1.RoleCount {
	counts := map[asterismv1.WorkloadRole]uint32{}
	for _, w := range ws {
		counts[w.Role]++
	}
	out := make([]*asterismv1.RoleCount, 0, len(counts))
	for r, n := range counts {
		out = append(out, &asterismv1.RoleCount{Role: r, Count: n})
	}
	slices.SortFunc(out, func(a, b *asterismv1.RoleCount) int { return cmp.Compare(a.Role, b.Role) })
	return out
}

// ---------------------------------------------------------------------------
// Ports
// ---------------------------------------------------------------------------

// effectivePort is the host port when published, otherwise the container port.
func effectivePort(p *asterismv1.PortBinding) uint32 {
	if p.HostPort > 0 {
		return p.HostPort
	}
	return p.ContainerPort
}

var portSorter = newSorter("port", "exposure, port",
	func(p *asterismv1.PortBinding) string { return p.Id },
	by("exposure", func(p *asterismv1.PortBinding) asterismv1.PortExposure { return p.Exposure }),
	by("port", effectivePort),
	byFold("workload", func(p *asterismv1.PortBinding) string { return p.WorkloadName }),
	by("project", func(p *asterismv1.PortBinding) string { return p.ProjectId }),
	by("proto", func(p *asterismv1.PortBinding) string { return p.Proto }),
)

// ListPortsPage serves InventoryService.ListPorts.
func (o *Observatory) ListPortsPage(req *asterismv1.ListPortsRequest) (*asterismv1.ListPortsResponse, error) {
	q := normQuery(req.GetQuery())
	port, isPort := portQuery(q)
	filters := []filter[*asterismv1.PortBinding]{
		{facet: "exposure", match: func(p *asterismv1.PortBinding) bool {
			return req.GetExposure() == asterismv1.PortExposure_PORT_EXPOSURE_UNSPECIFIED || p.Exposure == req.GetExposure()
		}},
		{match: func(p *asterismv1.PortBinding) bool {
			if isPort && (p.HostPort == port || p.ContainerPort == port) {
				return true
			}
			return containsAny(q, p.WorkloadName, p.WorkloadId, p.ProcessName, p.HostIp, p.ProjectId, p.Proto)
		}},
	}
	all := o.ListPorts(asterismv1.PortExposure_PORT_EXPOSURE_UNSPECIFIED)
	items, info, err := listPage(matching(all, filters, ""), portSorter, req.GetOrderBy(), req.GetPage())
	if err != nil {
		return nil, err
	}
	counts := &asterismv1.PortExposureCounts{}
	for _, p := range matching(all, filters, "exposure") {
		counts.Total++
		switch p.Exposure {
		case asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC:
			counts.Public++
		case asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK:
			counts.Loopback++
		case asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL:
			counts.Internal++
		}
	}
	return &asterismv1.ListPortsResponse{Ports: items, PageInfo: info, ExposureCounts: counts}, nil
}

// ---------------------------------------------------------------------------
// Ingress routes
// ---------------------------------------------------------------------------

func firstHostname(r *asterismv1.IngressRoute) string {
	if len(r.Hostnames) == 0 {
		return ""
	}
	return strings.ToLower(r.Hostnames[0])
}

var ingressSorter = newSorter("ingress route", "hostname",
	func(r *asterismv1.IngressRoute) string { return r.Id },
	by("hostname", firstHostname),
	by("port", func(r *asterismv1.IngressRoute) uint32 { return r.ListenPort }),
	by("proxy", func(r *asterismv1.IngressRoute) asterismv1.ProxyKind { return r.ProxyKind }),
	by("upstreams", func(r *asterismv1.IngressRoute) int { return len(r.Upstreams) }),
)

// ListIngressPage serves InventoryService.ListIngress.
func (o *Observatory) ListIngressPage(req *asterismv1.ListIngressRequest) (*asterismv1.ListIngressResponse, error) {
	q := normQuery(req.GetQuery())
	filters := []filter[*asterismv1.IngressRoute]{
		{match: func(r *asterismv1.IngressRoute) bool {
			if q == "" {
				return true
			}
			fields := append([]string{r.PathPrefix, r.Strategy, r.ProxyWorkloadId, r.Id}, r.Hostnames...)
			for _, u := range r.Upstreams {
				fields = append(fields, u.Address, u.WorkloadId)
			}
			return containsAny(q, fields...)
		}},
	}
	items, info, err := listPage(matching(o.ListIngress(), filters, ""), ingressSorter, req.GetOrderBy(), req.GetPage())
	if err != nil {
		return nil, err
	}
	return &asterismv1.ListIngressResponse{Routes: items, PageInfo: info}, nil
}

// ---------------------------------------------------------------------------
// Events
// ---------------------------------------------------------------------------

// severityBucket maps free-form severities onto the info/warning/critical facets.
func severityBucket(s string) string {
	switch strings.ToLower(s) {
	case "critical", "error":
		return "critical"
	case "warning", "warn":
		return "warning"
	default:
		return "info"
	}
}

func severityRank(s string) int {
	switch severityBucket(s) {
	case "critical":
		return 2
	case "warning":
		return 1
	default:
		return 0
	}
}

// kindGroup maps an event kind onto the lifecycle/audit/other facets.
func kindGroup(kind string) string {
	switch {
	case strings.HasPrefix(kind, "audit."):
		return "audit"
	case strings.HasPrefix(kind, "container."):
		return "lifecycle"
	default:
		return "other"
	}
}

var eventSorter = newSorter("event", "ts desc",
	func(e *asterismv1.Event) string { return e.Id },
	by("ts", func(e *asterismv1.Event) int64 { return e.TsMs }),
	by("severity", func(e *asterismv1.Event) int { return severityRank(e.Severity) }),
	by("kind", func(e *asterismv1.Event) string { return e.Kind }),
	byFold("entity", func(e *asterismv1.Event) string { return e.EntityName }),
)

// allEvents returns collector and service events for an entity (all when empty).
func (o *Observatory) allEvents(entityID string) []*asterismv1.Event {
	var inv []*asterismv1.Event
	if o.inventory != nil {
		inv = o.inventory.Events()
	}
	own := o.events.List(entityID, 0)
	out := make([]*asterismv1.Event, 0, len(inv)+len(own))
	for _, e := range inv {
		if entityID == "" || e.EntityId == entityID {
			out = append(out, e)
		}
	}
	return append(out, own...)
}

// ListEventsPage serves EventsService.ListEvents.
func (o *Observatory) ListEventsPage(req *asterismv1.ListEventsRequest) (*asterismv1.ListEventsResponse, error) {
	sev := map[string]bool{}
	for _, s := range req.GetSeverities() {
		s = strings.ToLower(strings.TrimSpace(s))
		switch s {
		case "":
			continue
		case "info", "warning", "critical", "error":
			sev[severityBucket(s)] = true
		default:
			return nil, newErr(ErrInvalidArgument, "unknown severity %q; allowed: info, warning, critical", s)
		}
	}
	group := strings.ToLower(strings.TrimSpace(req.GetKindGroup()))
	switch group {
	case "", "all":
		group = ""
	case "alerts":
		group = "other"
	case "lifecycle", "audit", "other":
	default:
		return nil, newErr(ErrInvalidArgument, "unknown kind_group %q; allowed: all, lifecycle, audit, other", group)
	}
	q := normQuery(req.GetQuery())
	filters := []filter[*asterismv1.Event]{
		{facet: "severity", match: func(e *asterismv1.Event) bool { return len(sev) == 0 || sev[severityBucket(e.Severity)] }},
		{facet: "kind", match: func(e *asterismv1.Event) bool { return group == "" || kindGroup(e.Kind) == group }},
		{match: func(e *asterismv1.Event) bool { return containsAny(q, e.Message, e.Kind, e.EntityName, e.EntityId) }},
	}
	all := o.allEvents(req.GetEntityId())
	items, info, err := listPage(matching(all, filters, ""), eventSorter, req.GetOrderBy(), legacyPage(req.GetPage(), req.GetLimit()))
	if err != nil {
		return nil, err
	}
	sevCounts := &asterismv1.EventSeverityCounts{}
	for _, e := range matching(all, filters, "severity") {
		sevCounts.Total++
		switch severityBucket(e.Severity) {
		case "critical":
			sevCounts.Critical++
		case "warning":
			sevCounts.Warning++
		default:
			sevCounts.Info++
		}
	}
	kindCounts := &asterismv1.EventKindCounts{}
	for _, e := range matching(all, filters, "kind") {
		kindCounts.Total++
		switch kindGroup(e.Kind) {
		case "audit":
			kindCounts.Audit++
		case "lifecycle":
			kindCounts.Lifecycle++
		default:
			kindCounts.Other++
		}
	}
	return &asterismv1.ListEventsResponse{Events: items, PageInfo: info, SeverityCounts: sevCounts, KindCounts: kindCounts}, nil
}

// ---------------------------------------------------------------------------
// Logs (cursor-based)
// ---------------------------------------------------------------------------

// Log page sizes for TailLogs.
const (
	DefaultLogLimit = 200
	MaxLogLimit     = 500
)

// TailLogs returns up to limit matching lines strictly older than
// before_ts_ms (newest when 0), oldest first, with a cursor for the next
// older page. A page never splits lines that share a timestamp, so the
// exclusive timestamp cursor cannot skip lines.
func (o *Observatory) TailLogs(req *asterismv1.TailLogsRequest) *asterismv1.TailLogsResponse {
	resp := &asterismv1.TailLogsResponse{Lines: []*asterismv1.LogLine{}}
	if o.inventory == nil {
		return resp
	}
	limit := int(req.GetLimit())
	switch {
	case limit <= 0:
		limit = DefaultLogLimit
	case limit > MaxLogLimit:
		limit = MaxLogLimit
	}
	q := normQuery(req.GetGrep())
	before := req.GetBeforeTsMs()
	src := o.inventory.Logs(req.GetWorkloadId())
	lines := make([]*asterismv1.LogLine, 0, len(src))
	for _, l := range src {
		if before > 0 && l.TsMs >= before {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(l.Text), q) {
			continue
		}
		lines = append(lines, l)
	}
	slices.SortStableFunc(lines, func(a, b *asterismv1.LogLine) int { return cmp.Compare(a.TsMs, b.TsMs) })
	cut := logCut(lines, limit)
	resp.Lines = lines[cut:]
	if cut > 0 {
		resp.HasMore = true
		resp.NextBeforeTsMs = resp.Lines[0].TsMs
	}
	return resp
}

// logCut returns the index where the newest page of at most limit lines
// starts in ts-ascending lines, moved so it never splits a timestamp group.
// If one group alone exceeds limit, the whole group is returned.
func logCut(lines []*asterismv1.LogLine, limit int) int {
	if len(lines) <= limit {
		return 0
	}
	cut := len(lines) - limit
	ts := lines[cut].TsMs
	if lines[cut-1].TsMs != ts {
		return cut
	}
	// Shrink: drop the partial group from this page.
	shrunk := cut
	for shrunk < len(lines) && lines[shrunk].TsMs == ts {
		shrunk++
	}
	if shrunk < len(lines) {
		return shrunk
	}
	// Every line on the page shares ts: extend back to include the whole group.
	for cut > 0 && lines[cut-1].TsMs == ts {
		cut--
	}
	return cut
}
