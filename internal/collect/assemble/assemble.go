// Package assemble turns normalized host facts (containers, host workloads,
// listening sockets, proxy routes, project rules) into the topology the
// service layer serves: projects with role-classified workloads, ports,
// ingress routes, edges, and inventory-derived events.
//
// Both collectors that see a whole host use it: the SSH bridge (which parses
// `docker ps`, `ss`, and nginx output into Input) and agent ingest (which
// converts agent inventory into Input). Classification lives here, in the
// hub, so classifier changes never need agent upgrades (docs/BLUEPRINT.md
// decision D24).
package assemble

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

// ContainerFact is one container as `docker ps` describes it.
type ContainerFact struct {
	Name  string
	Image string
	// Status is the human `docker ps` status, e.g. "Up 3 hours (healthy)".
	Status string
	// PortsRaw is the `docker ps` ports column, e.g.
	// "0.0.0.0:8080->80/tcp, 5432/tcp".
	PortsRaw   string
	Project    string
	Service    string
	WorkingDir string
	Networks   []string
}

// Listener is one listening socket on the host.
type Listener struct {
	Proto   string
	HostIP  string
	Port    uint32
	Process string
	// WorkloadID attributes the socket to a workload; empty means "host".
	WorkloadID string
}

// ProxyRoute is one reverse-proxy site with its raw upstream addresses.
type ProxyRoute struct {
	Kind asterismv1.ProxyKind
	// ProxyWorkloadID is the proxy's workload id. Edges from the proxy are
	// drawn only when that workload exists.
	ProxyWorkloadID string
	Hostnames       []string
	// ListenPort defaults to 443 with TLS, otherwise 80.
	ListenPort uint32
	TLS        bool
	// PathPrefix defaults to "/".
	PathPrefix string
	// Upstreams are addresses such as "127.0.0.1:3000", "http://[::1]:8080/",
	// or "shop-api:8080" (a container name or compose service).
	Upstreams []string
}

// HostWorkload is a non-container workload (systemd unit, process) placed in
// a project. Its ports are host listeners it owns.
type HostWorkload struct {
	Workload        *asterismv1.Workload
	Project         string
	ProjectKind     string
	ProjectCategory string
	WorkingDir      string
}

// LiveUsage carries live resource usage over an inventory rebuild.
type LiveUsage struct {
	CPUUtil       float64
	MemUsedBytes  uint64
	MemLimitBytes uint64
}

// Input is every fact Build needs. Build takes ownership of the messages it
// is given (Inspected containers get their Role set).
type Input struct {
	Now          time.Time
	HostMemTotal uint64
	// HostWorkloads are processed before containers.
	HostWorkloads []HostWorkload
	Containers    []ContainerFact
	// Inspected are full container records matched to Containers by name.
	Inspected []*asterismv1.Container
	Listeners []Listener
	Routes    []ProxyRoute
	// Rules move member workloads into named projects (agent `projects:`).
	Rules []*asterismv1.ProjectRule
	// Prev keeps live CPU and memory numbers across rebuilds, by workload id.
	Prev map[string]LiveUsage
	// Sync, when set, returns a summary event appended last.
	Sync func(projects, workloads, routes int) *asterismv1.Event
}

// Output is the assembled topology. Every message is newly allocated.
type Output struct {
	Projects  []*asterismv1.Project
	Workloads map[string]*asterismv1.Workload
	Ports     []*asterismv1.PortBinding
	Ingress   []*asterismv1.IngressRoute
	Edges     []*asterismv1.TopologyEdge
	Events    []*asterismv1.Event
	// Containers are the Inspected records with Role resolved.
	Containers []*asterismv1.Container
}

// Build assembles the topology.
func Build(in Input) Output {
	nowMs := in.Now.UnixMilli()

	inspectByName := make(map[string]*asterismv1.Container, len(in.Inspected))
	for _, ic := range in.Inspected {
		inspectByName[ic.Name] = ic
	}

	workloadsByID := make(map[string]*asterismv1.Workload)
	projectsMap := make(map[string]*asterismv1.Project)
	loopbackPortOwner := make(map[uint32]*asterismv1.Workload)
	var allPorts []*asterismv1.PortBinding
	seenPortKeys := make(map[string]bool)

	getOrCreateProject := func(name, workDir string) *asterismv1.Project {
		if name == "" {
			name = "standalone"
		}
		id := "proj:" + name
		if p, ok := projectsMap[id]; ok {
			if p.WorkingDir == "" && workDir != "" {
				p.WorkingDir = workDir
			}
			return p
		}
		p := &asterismv1.Project{
			Id:          id,
			Name:        name,
			Kind:        "compose",
			Category:    ClassifyProjectCategory(name),
			WorkingDir:  workDir,
			Status:      asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY,
			HealthScore: 100,
		}
		projectsMap[id] = p
		return p
	}

	// Host workloads (proxy units first, then other units and processes).
	for _, hw := range in.HostWorkloads {
		wl := hw.Workload
		if wl == nil || wl.Id == "" {
			continue
		}
		proj := getOrCreateProject(hw.Project, hw.WorkingDir)
		if hw.ProjectKind != "" {
			proj.Kind = hw.ProjectKind
		}
		if hw.ProjectCategory != "" {
			proj.Category = hw.ProjectCategory
		}
		wl.ProjectId = proj.Id
		if prev, ok := in.Prev[wl.Id]; ok {
			wl.CpuUtil = prev.CPUUtil
			wl.MemUsedBytes = prev.MemUsedBytes
			wl.MemLimitBytes = prev.MemLimitBytes
		}
		for _, pb := range wl.Ports {
			pb.ProjectId = proj.Id
			allPorts = append(allPorts, pb)
			seenPortKeys[fmt.Sprintf("%s:%s:%d", pb.Proto, pb.HostIp, pb.HostPort)] = true
			if pb.HostIp == "0.0.0.0" {
				seenPortKeys[fmt.Sprintf("%s:[::]:%d", pb.Proto, pb.HostPort)] = true
			}
		}
		proj.Workloads = append(proj.Workloads, wl)
		workloadsByID[wl.Id] = wl
	}

	var events []*asterismv1.Event
	var platformDB, platformStorage *asterismv1.Workload

	for _, ctr := range in.Containers {
		// Skip one-shot completed migration containers from cluttering the active architecture
		if strings.HasPrefix(ctr.Status, "Exited (0)") && strings.Contains(strings.ToLower(ctr.Service), "migrate") {
			continue
		}

		proj := getOrCreateProject(ctr.Project, ctr.WorkingDir)
		wlID := "ctr:" + ctr.Name
		health, state := ParseDockerStatus(ctr.Status)
		role := ClassifyWorkloadRole(ctr.Name, ctr.Service, ctr.Image, ctr.PortsRaw)

		wl := &asterismv1.Workload{
			Id:          wlID,
			Kind:        asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER,
			Role:        role,
			Name:        ctr.Name,
			ProjectId:   proj.Id,
			ServiceName: ctr.Service,
			Image:       ctr.Image,
			State:       state,
			Health:      health,
			Replicas:    1,
			Networks:    ctr.Networks,
		}
		if prev, ok := in.Prev[wlID]; ok {
			wl.CpuUtil = prev.CPUUtil
			wl.MemUsedBytes = prev.MemUsedBytes
			wl.MemLimitBytes = prev.MemLimitBytes
		}
		if ic, ok := inspectByName[ctr.Name]; ok {
			if r := RoleFromLabel(ic.Labels["asterism.role"]); r != asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED {
				wl.Role = r
				role = r
			}
			ic.Role = role
			wl.RestartCount = ic.RestartCount
			wl.StartedAtMs = ic.StartedAtMs
			wl.Pid = ic.Pid
			wl.EnvKeys = ic.EnvKeys
			wl.Labels = ic.Labels
			if wl.MemLimitBytes == 0 && ic.Resources != nil {
				wl.MemLimitBytes = ic.Resources.MemLimitBytes
			}
		}

		parsedPorts := ParseDockerPorts(ctr.PortsRaw, wlID, ctr.Name, proj.Id)
		wl.Ports = parsedPorts
		for _, pb := range parsedPorts {
			allPorts = append(allPorts, pb)
			if pb.HostPort > 0 {
				loopbackPortOwner[pb.HostPort] = wl
				seenPortKeys[fmt.Sprintf("%s:%s:%d", pb.Proto, pb.HostIp, pb.HostPort)] = true
			}
		}

		proj.Workloads = append(proj.Workloads, wl)
		workloadsByID[wlID] = wl

		if ctr.Project == "platform" && role == asterismv1.WorkloadRole_WORKLOAD_ROLE_DATABASE {
			platformDB = wl
		}
		if ctr.Project == "platform" && (role == asterismv1.WorkloadRole_WORKLOAD_ROLE_STORAGE || strings.Contains(ctr.Name, "garage")) {
			platformStorage = wl
		}

		if health == asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED {
			events = append(events, &asterismv1.Event{
				Id:         "evt:unhealthy:" + ctr.Name,
				TsMs:       nowMs - 60_000,
				EntityId:   wlID,
				EntityName: ctr.Name,
				Kind:       "container.health",
				Severity:   "warning",
				Message:    fmt.Sprintf("Container status: %s", ctr.Status),
			})
		} else if health == asterismv1.HealthStatus_HEALTH_STATUS_DOWN {
			events = append(events, &asterismv1.Event{
				Id:         "evt:down:" + ctr.Name,
				TsMs:       nowMs - 120_000,
				EntityId:   wlID,
				EntityName: ctr.Name,
				Kind:       "container.state",
				Severity:   "critical",
				Message:    fmt.Sprintf("Container stopped: %s", ctr.Status),
			})
		} else if strings.Contains(ctr.Status, "seconds") || strings.Contains(ctr.Status, "minute") {
			events = append(events, &asterismv1.Event{
				Id:         "evt:recent:" + ctr.Name,
				TsMs:       nowMs - 30_000,
				EntityId:   wlID,
				EntityName: ctr.Name,
				Kind:       "container.start",
				Severity:   "info",
				Message:    fmt.Sprintf("Recently started (%s)", ctr.Status),
			})
		}
	}

	// Add remaining host listening ports (sshd, tailscaled, bare host daemons)
	for _, rp := range in.Listeners {
		key := fmt.Sprintf("%s:%s:%d", rp.Proto, rp.HostIP, rp.Port)
		if seenPortKeys[key] {
			continue
		}
		// Skip duplicate docker-proxy listeners already linked to containers
		if rp.Process == "docker-proxy" && loopbackPortOwner[rp.Port] != nil {
			continue
		}
		seenPortKeys[key] = true
		owner := "host"
		if rp.WorkloadID != "" {
			owner = rp.WorkloadID
		}
		pb := &asterismv1.PortBinding{
			Id:            fmt.Sprintf("port:host:%s:%s:%d", rp.Proto, rp.HostIP, rp.Port),
			WorkloadId:    owner,
			WorkloadName:  rp.Process,
			HostIp:        rp.HostIP,
			HostPort:      rp.Port,
			ContainerPort: rp.Port,
			Proto:         rp.Proto,
			Exposure:      dockercli.ClassifyExposure(rp.HostIP),
			ProcessName:   rp.Process,
		}
		if w, ok := workloadsByID[owner]; ok {
			pb.ProjectId = w.ProjectId
		}
		allPorts = append(allPorts, pb)
	}

	applyRules(in.Rules, workloadsByID, projectsMap, getOrCreateProject)

	// Build IngressRoutes & proxy edges
	var ingressRoutes []*asterismv1.IngressRoute
	var edges []*asterismv1.TopologyEdge
	edgeSeen := make(map[string]bool)
	addEdge := func(e *asterismv1.TopologyEdge) {
		if edgeSeen[e.Id] || e.SourceId == e.TargetId {
			return
		}
		edgeSeen[e.Id] = true
		edges = append(edges, e)
	}

	for _, site := range in.Routes {
		if len(site.Hostnames) == 0 {
			continue
		}
		kind := proxySlug(site.Kind)
		_, proxyActive := workloadsByID[site.ProxyWorkloadID]
		var upstreams []*asterismv1.UpstreamTarget
		for _, addr := range site.Upstreams {
			host, targetPort := splitUpstream(addr)
			owner := resolveUpstream(host, targetPort, loopbackPortOwner, workloadsByID)
			wlID := "host"
			if owner != nil {
				wlID = owner.Id
				if proxyActive {
					addEdge(&asterismv1.TopologyEdge{
						Id:        fmt.Sprintf("edge:%s:%s:%d", kind, owner.Id, targetPort),
						SourceId:  site.ProxyWorkloadID,
						TargetId:  owner.Id,
						Proto:     "tcp",
						Port:      targetPort,
						EdgeKind:  asterismv1.EdgeKind_EDGE_KIND_INGRESS,
						Source:    asterismv1.EdgeSource_EDGE_SOURCE_PROXY,
						Label:     fmt.Sprintf("%s :%d", site.Hostnames[0], targetPort),
						ConnCount: 1,
						Weight:    1,
					})
				}
			}
			upstreams = append(upstreams, &asterismv1.UpstreamTarget{
				WorkloadId: wlID,
				Address:    addr,
				Weight:     1,
				Healthy:    owner == nil || owner.Health != asterismv1.HealthStatus_HEALTH_STATUS_DOWN,
			})
		}
		if len(upstreams) == 0 {
			continue
		}
		strategy := "single"
		if len(upstreams) > 1 {
			strategy = "upstream_pool"
		}
		listen := site.ListenPort
		if listen == 0 {
			listen = 80
			if site.TLS {
				listen = 443
			}
		}
		prefix := site.PathPrefix
		if prefix == "" {
			prefix = "/"
		}
		ingressRoutes = append(ingressRoutes, &asterismv1.IngressRoute{
			Id:              "ingress:" + kind + "/" + site.Hostnames[0],
			ProxyKind:       site.Kind,
			ProxyWorkloadId: site.ProxyWorkloadID,
			Hostnames:       site.Hostnames,
			ListenPort:      listen,
			Tls:             site.TLS,
			PathPrefix:      prefix,
			Strategy:        strategy,
			Upstreams:       upstreams,
		})
	}

	// Build intra-project & shared platform database/storage edges
	for _, proj := range projectsMap {
		var gateways, apis, workers, caches []*asterismv1.Workload
		for _, wl := range proj.Workloads {
			switch wl.Role {
			case asterismv1.WorkloadRole_WORKLOAD_ROLE_PROXY:
				if wl.Kind != asterismv1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT {
					gateways = append(gateways, wl)
				}
			case asterismv1.WorkloadRole_WORKLOAD_ROLE_APP:
				if strings.Contains(wl.ServiceName, "web") || strings.Contains(wl.ServiceName, "admin") ||
					strings.Contains(wl.ServiceName, "site") || strings.Contains(wl.ServiceName, "dapp") ||
					strings.Contains(wl.ServiceName, "gateway") {
					gateways = append(gateways, wl)
				} else {
					apis = append(apis, wl)
				}
			case asterismv1.WorkloadRole_WORKLOAD_ROLE_WORKER:
				workers = append(workers, wl)
			case asterismv1.WorkloadRole_WORKLOAD_ROLE_CACHE, asterismv1.WorkloadRole_WORKLOAD_ROLE_QUEUE:
				caches = append(caches, wl)
			}

			// Shared platform network -> platform-postgres / platform-garage
			if proj.Name != "platform" && hasString(wl.Networks, "platform") {
				if platformDB != nil {
					addEdge(&asterismv1.TopologyEdge{
						Id:       fmt.Sprintf("edge:pg:%s", wl.Id),
						SourceId: wl.Id,
						TargetId: platformDB.Id,
						Proto:    "tcp",
						Port:     5432,
						EdgeKind: asterismv1.EdgeKind_EDGE_KIND_DATABASE,
						Source:   asterismv1.EdgeSource_EDGE_SOURCE_STATIC,
						Label:    "platform-pg :5432",
					})
				}
				if platformStorage != nil && (wl.Role == asterismv1.WorkloadRole_WORKLOAD_ROLE_APP || wl.Role == asterismv1.WorkloadRole_WORKLOAD_ROLE_WORKER) && strings.Contains(strings.ToLower(wl.Name), "api") {
					addEdge(&asterismv1.TopologyEdge{
						Id:       fmt.Sprintf("edge:s3:%s", wl.Id),
						SourceId: wl.Id,
						TargetId: platformStorage.Id,
						Proto:    "tcp",
						Port:     3900,
						EdgeKind: asterismv1.EdgeKind_EDGE_KIND_NETWORK,
						Source:   asterismv1.EdgeSource_EDGE_SOURCE_STATIC,
						Label:    "garage S3 :3900",
					})
				}
			}
		}

		// Connect web/gateway -> api -> caches/queues within the same project
		for _, gw := range gateways {
			for _, api := range apis {
				addEdge(&asterismv1.TopologyEdge{
					Id:       fmt.Sprintf("edge:app:%s:%s", gw.Id, api.Id),
					SourceId: gw.Id,
					TargetId: api.Id,
					Proto:    "tcp",
					EdgeKind: asterismv1.EdgeKind_EDGE_KIND_APP,
					Source:   asterismv1.EdgeSource_EDGE_SOURCE_STATIC,
					Label:    "internal HTTP",
				})
			}
		}
		for _, svc := range append(apis, workers...) {
			for _, cache := range caches {
				addEdge(&asterismv1.TopologyEdge{
					Id:       fmt.Sprintf("edge:cache:%s:%s", svc.Id, cache.Id),
					SourceId: svc.Id,
					TargetId: cache.Id,
					Proto:    "tcp",
					EdgeKind: asterismv1.EdgeKind_EDGE_KIND_CACHE,
					Source:   asterismv1.EdgeSource_EDGE_SOURCE_STATIC,
					Label:    cache.ServiceName,
				})
			}
		}
	}

	// Finalize project rollups and health scores
	projects := make([]*asterismv1.Project, 0, len(projectsMap))
	for _, proj := range projectsMap {
		Rollup(proj, in.HostMemTotal)
		projects = append(projects, proj)
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Category != projects[j].Category {
			return categoryOrder(projects[i].Category) < categoryOrder(projects[j].Category)
		}
		return projects[i].MemUsedBytes > projects[j].MemUsedBytes
	})

	sort.Slice(allPorts, func(i, j int) bool {
		if allPorts[i].Exposure != allPorts[j].Exposure {
			return allPorts[i].Exposure < allPorts[j].Exposure
		}
		return allPorts[i].HostPort < allPorts[j].HostPort
	})

	sort.Slice(ingressRoutes, func(i, j int) bool {
		return ingressRoutes[i].Hostnames[0] < ingressRoutes[j].Hostnames[0]
	})

	if in.Sync != nil {
		if ev := in.Sync(len(projects), len(workloadsByID), len(ingressRoutes)); ev != nil {
			events = append(events, ev)
		}
	}

	for _, ic := range in.Inspected {
		if ic.Role == asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED {
			ic.Role = ClassifyWorkloadRole(ic.Name, ic.ComposeService, ic.Image, "")
			if r := RoleFromLabel(ic.Labels["asterism.role"]); r != asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED {
				ic.Role = r
			}
		}
	}

	return Output{
		Projects:   projects,
		Workloads:  workloadsByID,
		Ports:      allPorts,
		Ingress:    ingressRoutes,
		Edges:      edges,
		Events:     events,
		Containers: in.Inspected,
	}
}

// Rollup recomputes a project's usage totals, health score, and status from
// its workloads (sorted by name).
func Rollup(proj *asterismv1.Project, hostMemTotal uint64) {
	sort.Slice(proj.Workloads, func(i, j int) bool { return proj.Workloads[i].Name < proj.Workloads[j].Name })
	proj.WorkloadCount = uint32(len(proj.Workloads))
	var cpuSum float64
	var memSum, limitSum uint64
	var unhealthyCount, downCount int
	for _, wl := range proj.Workloads {
		cpuSum += wl.CpuUtil
		memSum += wl.MemUsedBytes
		limitSum += wl.MemLimitBytes
		if wl.Health == asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED {
			unhealthyCount++
		} else if wl.Health == asterismv1.HealthStatus_HEALTH_STATUS_DOWN {
			downCount++
		}
	}
	proj.CpuUtil = cpuSum
	proj.MemUsedBytes = memSum
	proj.MemLimitBytes = limitSum
	if hostMemTotal > 0 {
		proj.MemSharePct = (float64(memSum) / float64(hostMemTotal)) * 100.0
	}
	score := 100 - unhealthyCount*18 - downCount*35
	if score < 10 {
		score = 10
	}
	proj.HealthScore = uint32(score)
	if downCount > 0 {
		proj.Status = asterismv1.HealthStatus_HEALTH_STATUS_DOWN
	} else if unhealthyCount > 0 {
		proj.Status = asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED
	} else {
		proj.Status = asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY
	}
}

// applyRules moves rule members into the rule's project, which takes the
// rule's category and carries its icon and colour as asterism.icon /
// asterism.color project labels. Projects left empty are dropped.
func applyRules(rules []*asterismv1.ProjectRule, workloads map[string]*asterismv1.Workload, projects map[string]*asterismv1.Project, getOrCreate func(name, workDir string) *asterismv1.Project) {
	for _, r := range rules {
		if r.GetName() == "" || len(r.GetMemberIds()) == 0 {
			continue
		}
		target := getOrCreate(r.GetName(), "")
		if r.GetCategory() != "" {
			target.Category = strings.ToLower(r.GetCategory())
		}
		if r.GetIcon() != "" || r.GetColor() != "" {
			if target.Labels == nil {
				target.Labels = map[string]string{}
			}
			if r.GetIcon() != "" {
				target.Labels["asterism.icon"] = r.GetIcon()
			}
			if r.GetColor() != "" {
				target.Labels["asterism.color"] = r.GetColor()
			}
		}
		for _, id := range r.GetMemberIds() {
			wl, ok := workloads[id]
			if !ok || wl.ProjectId == target.Id {
				continue
			}
			if from, ok := projects[wl.ProjectId]; ok {
				kept := from.Workloads[:0]
				for _, w := range from.Workloads {
					if w != wl {
						kept = append(kept, w)
					}
				}
				from.Workloads = kept
				if len(from.Workloads) == 0 {
					delete(projects, from.Id)
				}
			}
			wl.ProjectId = target.Id
			for _, pb := range wl.Ports {
				if pb.ProjectId != "" {
					pb.ProjectId = target.Id
				}
			}
			target.Workloads = append(target.Workloads, wl)
		}
		if len(target.Workloads) == 0 {
			delete(projects, target.Id)
		}
	}
}

// splitUpstream returns the host and port of an upstream address, accepting
// an optional scheme and path ("http://127.0.0.1:3000/api").
func splitUpstream(addr string) (string, uint32) {
	a := strings.TrimSpace(addr)
	if i := strings.Index(a, "://"); i >= 0 {
		a = a[i+3:]
	}
	if i := strings.IndexByte(a, '/'); i >= 0 {
		a = a[:i]
	}
	host, port, err := net.SplitHostPort(a)
	if err != nil {
		return a, 0
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return host, 0
	}
	return host, uint32(p)
}

func isLoopbackOrAny(host string) bool {
	switch strings.ToLower(host) {
	case "", "localhost", "0.0.0.0", "::", "*":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// resolveUpstream maps an upstream to a workload: loopback addresses by the
// published host port, other hosts by container name or compose service.
func resolveUpstream(host string, port uint32, byPort map[uint32]*asterismv1.Workload, byID map[string]*asterismv1.Workload) *asterismv1.Workload {
	if port == 0 {
		return nil
	}
	if isLoopbackOrAny(host) {
		return byPort[port]
	}
	if w, ok := byID["ctr:"+host]; ok {
		return w
	}
	var match *asterismv1.Workload
	for _, id := range dockercli.SortedKeys(byID) {
		w := byID[id]
		if w.Kind == asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER && w.ServiceName == host {
			if match != nil {
				return nil // ambiguous across projects
			}
			match = w
		}
	}
	return match
}

func proxySlug(k asterismv1.ProxyKind) string {
	switch k {
	case asterismv1.ProxyKind_PROXY_KIND_CADDY:
		return "caddy"
	case asterismv1.ProxyKind_PROXY_KIND_TRAEFIK:
		return "traefik"
	case asterismv1.ProxyKind_PROXY_KIND_CLOUDFLARED:
		return "cloudflared"
	case asterismv1.ProxyKind_PROXY_KIND_NGINX:
		return "nginx"
	case asterismv1.ProxyKind_PROXY_KIND_HAPROXY:
		return "haproxy"
	default:
		return "proxy"
	}
}

func hasString(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

func categoryOrder(cat string) int {
	switch cat {
	case "infra":
		return 0
	case "production":
		return 1
	case "staging":
		return 2
	default:
		return 3
	}
}
