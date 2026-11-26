package sshbridge

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/assemble"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

// legacyResult is what the pre-extraction collectInventory stored.
type legacyResult struct {
	projects   []*asterismv1.Project
	workloads  map[string]*asterismv1.Workload
	ports      []*asterismv1.PortBinding
	ingress    []*asterismv1.IngressRoute
	edges      []*asterismv1.TopologyEdge
	events     []*asterismv1.Event
	containers []*asterismv1.Container
}

// legacyInventory is the SSH bridge's inventory assembly exactly as it was
// before internal/collect/assemble existed, frozen here so the parity test
// can prove the extraction changed no behaviour. The pure classifiers it
// calls were moved verbatim, so they are shared.
func legacyInventory(target string, sections map[string]string, now time.Time, prevWorkloads map[string]assemble.LiveUsage, hostMemTotal uint64) legacyResult {
	nowMs := now.UnixMilli()
	nginxActive := strings.TrimSpace(sections["NGINX_ACTIVE"]) == "active"
	rawContainers := parseDockerPS(sections["DOCKER"])
	inspected := dockercli.ParseInspect(sections["DINSPECT"], now)
	inspectByName := make(map[string]*asterismv1.Container, len(inspected))
	for _, ic := range inspected {
		inspectByName[ic.Name] = ic
	}
	rawPorts := parseSSPorts(sections["PORTS"])
	nginxSites := parseNginxSites(sections["NGINX_SITES"])

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
		p := &asterismv1.Project{Id: id, Name: name, Kind: "compose", Category: assemble.ClassifyProjectCategory(name), WorkingDir: workDir,
			Status: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, HealthScore: 100}
		projectsMap[id] = p
		return p
	}

	if nginxActive {
		edgeProj := getOrCreateProject("edge-ingress", "/etc/nginx/sites-enabled")
		edgeProj.Kind = "systemd"
		edgeProj.Category = "infra"
		nginxWl := &asterismv1.Workload{
			Id: "unit:nginx.service", Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_SYSTEMD_UNIT, Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_PROXY,
			Name: "nginx", ProjectId: edgeProj.Id, ServiceName: "nginx", Image: "host systemd reverse proxy", State: "active",
			Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, Replicas: 1, Networks: []string{"host"},
		}
		for _, portNum := range []uint32{80, 443} {
			pb := &asterismv1.PortBinding{
				Id: fmt.Sprintf("port:nginx:%d", portNum), WorkloadId: nginxWl.Id, WorkloadName: "nginx", ProjectId: edgeProj.Id,
				HostIp: "0.0.0.0", HostPort: portNum, ContainerPort: portNum, Proto: "tcp",
				Exposure: asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC, ProcessName: "nginx",
			}
			nginxWl.Ports = append(nginxWl.Ports, pb)
			allPorts = append(allPorts, pb)
			seenPortKeys[fmt.Sprintf("tcp:0.0.0.0:%d", portNum)] = true
			seenPortKeys[fmt.Sprintf("tcp:[::]:%d", portNum)] = true
		}
		edgeProj.Workloads = append(edgeProj.Workloads, nginxWl)
		workloadsByID[nginxWl.Id] = nginxWl
	}

	var events []*asterismv1.Event
	var platformDB, platformStorage *asterismv1.Workload
	for _, ctr := range rawContainers {
		if strings.HasPrefix(ctr.status, "Exited (0)") && strings.Contains(strings.ToLower(ctr.service), "migrate") {
			continue
		}
		proj := getOrCreateProject(ctr.project, ctr.workingDir)
		wlID := "ctr:" + ctr.name
		health, state := parseDockerStatus(ctr.status)
		role := assemble.ClassifyWorkloadRole(ctr.name, ctr.service, ctr.image, ctr.portsRaw)
		wl := &asterismv1.Workload{Id: wlID, Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER, Role: role, Name: ctr.name, ProjectId: proj.Id,
			ServiceName: ctr.service, Image: ctr.image, State: state, Health: health, Replicas: 1, Networks: ctr.networks}
		if prev, ok := prevWorkloads[wlID]; ok {
			wl.CpuUtil, wl.MemUsedBytes, wl.MemLimitBytes = prev.CPUUtil, prev.MemUsedBytes, prev.MemLimitBytes
		}
		if ic, ok := inspectByName[ctr.name]; ok {
			if r := assemble.RoleFromLabel(ic.Labels["asterism.role"]); r != asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED {
				wl.Role = r
				role = r
			}
			ic.Role = role
			wl.RestartCount, wl.StartedAtMs, wl.Pid, wl.EnvKeys, wl.Labels = ic.RestartCount, ic.StartedAtMs, ic.Pid, ic.EnvKeys, ic.Labels
			if wl.MemLimitBytes == 0 && ic.Resources != nil {
				wl.MemLimitBytes = ic.Resources.MemLimitBytes
			}
		}
		parsedPorts := parseDockerPorts(ctr.portsRaw, wlID, ctr.name, proj.Id)
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
		if ctr.project == "platform" && role == asterismv1.WorkloadRole_WORKLOAD_ROLE_DATABASE {
			platformDB = wl
		}
		if ctr.project == "platform" && (role == asterismv1.WorkloadRole_WORKLOAD_ROLE_STORAGE || strings.Contains(ctr.name, "garage")) {
			platformStorage = wl
		}
		if health == asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED {
			events = append(events, &asterismv1.Event{Id: "evt:unhealthy:" + ctr.name, TsMs: nowMs - 60_000, EntityId: wlID, EntityName: ctr.name,
				Kind: "container.health", Severity: "warning", Message: fmt.Sprintf("Container status: %s", ctr.status)})
		} else if health == asterismv1.HealthStatus_HEALTH_STATUS_DOWN {
			events = append(events, &asterismv1.Event{Id: "evt:down:" + ctr.name, TsMs: nowMs - 120_000, EntityId: wlID, EntityName: ctr.name,
				Kind: "container.state", Severity: "critical", Message: fmt.Sprintf("Container stopped: %s", ctr.status)})
		} else if strings.Contains(ctr.status, "seconds") || strings.Contains(ctr.status, "minute") {
			events = append(events, &asterismv1.Event{Id: "evt:recent:" + ctr.name, TsMs: nowMs - 30_000, EntityId: wlID, EntityName: ctr.name,
				Kind: "container.start", Severity: "info", Message: fmt.Sprintf("Recently started (%s)", ctr.status)})
		}
	}

	for _, rp := range rawPorts {
		key := fmt.Sprintf("%s:%s:%d", rp.proto, rp.hostIP, rp.port)
		if seenPortKeys[key] {
			continue
		}
		if rp.process == "docker-proxy" && loopbackPortOwner[rp.port] != nil {
			continue
		}
		seenPortKeys[key] = true
		allPorts = append(allPorts, &asterismv1.PortBinding{
			Id: fmt.Sprintf("port:host:%s:%s:%d", rp.proto, rp.hostIP, rp.port), WorkloadId: "host", WorkloadName: rp.process,
			HostIp: rp.hostIP, HostPort: rp.port, ContainerPort: rp.port, Proto: rp.proto, Exposure: dockercli.ClassifyExposure(rp.hostIP), ProcessName: rp.process,
		})
	}

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
	for _, site := range nginxSites {
		if len(site.hostnames) == 0 {
			continue
		}
		var upstreams []*asterismv1.UpstreamTarget
		for _, targetPort := range site.targetPorts {
			owner := loopbackPortOwner[targetPort]
			wlID := "host"
			if owner != nil {
				wlID = owner.Id
				if nginxActive {
					addEdge(&asterismv1.TopologyEdge{Id: fmt.Sprintf("edge:nginx:%s:%d", owner.Id, targetPort), SourceId: "unit:nginx.service", TargetId: owner.Id,
						Proto: "tcp", Port: targetPort, EdgeKind: asterismv1.EdgeKind_EDGE_KIND_INGRESS, Source: asterismv1.EdgeSource_EDGE_SOURCE_PROXY,
						Label: fmt.Sprintf("%s :%d", site.hostnames[0], targetPort), ConnCount: 1, Weight: 1})
				}
			}
			upstreams = append(upstreams, &asterismv1.UpstreamTarget{WorkloadId: wlID, Address: fmt.Sprintf("127.0.0.1:%d", targetPort), Weight: 1,
				Healthy: owner == nil || owner.Health != asterismv1.HealthStatus_HEALTH_STATUS_DOWN})
		}
		if len(upstreams) == 0 {
			continue
		}
		strategy := "single"
		if len(upstreams) > 1 {
			strategy = "upstream_pool"
		}
		ingressRoutes = append(ingressRoutes, &asterismv1.IngressRoute{Id: "ingress:nginx/" + site.hostnames[0], ProxyKind: asterismv1.ProxyKind_PROXY_KIND_NGINX,
			ProxyWorkloadId: "unit:nginx.service", Hostnames: site.hostnames, ListenPort: site.listenPort, Tls: site.tls, PathPrefix: "/", Strategy: strategy, Upstreams: upstreams})
	}

	for _, proj := range projectsMap {
		var gateways, apis, workers, caches []*asterismv1.Workload
		for _, wl := range proj.Workloads {
			switch wl.Role {
			case asterismv1.WorkloadRole_WORKLOAD_ROLE_PROXY:
				if wl.Id != "unit:nginx.service" {
					gateways = append(gateways, wl)
				}
			case asterismv1.WorkloadRole_WORKLOAD_ROLE_APP:
				if strings.Contains(wl.ServiceName, "web") || strings.Contains(wl.ServiceName, "admin") || strings.Contains(wl.ServiceName, "site") ||
					strings.Contains(wl.ServiceName, "dapp") || strings.Contains(wl.ServiceName, "gateway") {
					gateways = append(gateways, wl)
				} else {
					apis = append(apis, wl)
				}
			case asterismv1.WorkloadRole_WORKLOAD_ROLE_WORKER:
				workers = append(workers, wl)
			case asterismv1.WorkloadRole_WORKLOAD_ROLE_CACHE, asterismv1.WorkloadRole_WORKLOAD_ROLE_QUEUE:
				caches = append(caches, wl)
			}
			if proj.Name != "platform" && hasStr(wl.Networks, "platform") {
				if platformDB != nil {
					addEdge(&asterismv1.TopologyEdge{Id: fmt.Sprintf("edge:pg:%s", wl.Id), SourceId: wl.Id, TargetId: platformDB.Id, Proto: "tcp", Port: 5432,
						EdgeKind: asterismv1.EdgeKind_EDGE_KIND_DATABASE, Source: asterismv1.EdgeSource_EDGE_SOURCE_STATIC, Label: "platform-pg :5432"})
				}
				if platformStorage != nil && (wl.Role == asterismv1.WorkloadRole_WORKLOAD_ROLE_APP || wl.Role == asterismv1.WorkloadRole_WORKLOAD_ROLE_WORKER) && strings.Contains(strings.ToLower(wl.Name), "api") {
					addEdge(&asterismv1.TopologyEdge{Id: fmt.Sprintf("edge:s3:%s", wl.Id), SourceId: wl.Id, TargetId: platformStorage.Id, Proto: "tcp", Port: 3900,
						EdgeKind: asterismv1.EdgeKind_EDGE_KIND_NETWORK, Source: asterismv1.EdgeSource_EDGE_SOURCE_STATIC, Label: "garage S3 :3900"})
				}
			}
		}
		for _, gw := range gateways {
			for _, api := range apis {
				addEdge(&asterismv1.TopologyEdge{Id: fmt.Sprintf("edge:app:%s:%s", gw.Id, api.Id), SourceId: gw.Id, TargetId: api.Id, Proto: "tcp",
					EdgeKind: asterismv1.EdgeKind_EDGE_KIND_APP, Source: asterismv1.EdgeSource_EDGE_SOURCE_STATIC, Label: "internal HTTP"})
			}
		}
		for _, svc := range append(apis, workers...) {
			for _, cache := range caches {
				addEdge(&asterismv1.TopologyEdge{Id: fmt.Sprintf("edge:cache:%s:%s", svc.Id, cache.Id), SourceId: svc.Id, TargetId: cache.Id, Proto: "tcp",
					EdgeKind: asterismv1.EdgeKind_EDGE_KIND_CACHE, Source: asterismv1.EdgeSource_EDGE_SOURCE_STATIC, Label: cache.ServiceName})
			}
		}
	}

	projects := make([]*asterismv1.Project, 0, len(projectsMap))
	for _, proj := range projectsMap {
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
		proj.CpuUtil, proj.MemUsedBytes, proj.MemLimitBytes = cpuSum, memSum, limitSum
		if hostMemTotal > 0 {
			proj.MemSharePct = (float64(memSum) / float64(hostMemTotal)) * 100.0
		}
		score := max(100-unhealthyCount*18-downCount*35, 10)
		proj.HealthScore = uint32(score)
		switch {
		case downCount > 0:
			proj.Status = asterismv1.HealthStatus_HEALTH_STATUS_DOWN
		case unhealthyCount > 0:
			proj.Status = asterismv1.HealthStatus_HEALTH_STATUS_DEGRADED
		default:
			proj.Status = asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY
		}
		projects = append(projects, proj)
	}
	events = append(events, &asterismv1.Event{Id: "evt:ssh:sync", TsMs: nowMs, EntityId: "host", EntityName: target, Kind: "ssh.sync", Severity: "info",
		Message: fmt.Sprintf("Synced %d projects, %d workloads, %d Nginx routes over SSH", len(projects), len(workloadsByID), len(ingressRoutes))})
	for _, ic := range inspected {
		if ic.Role == asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED {
			ic.Role = assemble.ClassifyWorkloadRole(ic.Name, ic.ComposeService, ic.Image, "")
			if r := assemble.RoleFromLabel(ic.Labels["asterism.role"]); r != asterismv1.WorkloadRole_WORKLOAD_ROLE_UNSPECIFIED {
				ic.Role = r
			}
		}
	}
	return legacyResult{projects, workloadsByID, allPorts, ingressRoutes, edges, events, inspected}
}

func hasStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// byID sorts a copy of msgs by id so map-order differences (which the
// legacy code also had) do not affect the comparison.
func byID[T proto.Message](msgs []T, id func(T) string) []T {
	out := append([]T(nil), msgs...)
	sort.SliceStable(out, func(i, j int) bool { return id(out[i]) < id(out[j]) })
	return out
}

func equalLists[T proto.Message](t *testing.T, what string, got, want []T, id func(T) string) {
	t.Helper()
	g, w := byID(got, id), byID(want, id)
	if len(g) != len(w) {
		t.Fatalf("%s: got %d, want %d", what, len(g), len(w))
	}
	for i := range g {
		if !proto.Equal(g[i], w[i]) {
			t.Errorf("%s[%s] differs:\n got  %v\n want %v", what, id(w[i]), g[i], w[i])
		}
	}
}

func TestAssembleParityWithLegacyInventory(t *testing.T) {
	raw, err := os.ReadFile("testdata/inventory.txt")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	prev := map[string]assemble.LiveUsage{
		"ctr:shop-api-1":          {CPUUtil: 0.25, MemUsedBytes: 300 << 20, MemLimitBytes: 1 << 30},
		"ctr:platform-postgres-1": {CPUUtil: 0.1, MemUsedBytes: 700 << 20},
	}
	const hostMem = 8 << 30

	c := New(slog.New(slog.NewTextHandler(io.Discard, nil)), "vps-1", bus.New(1), ring.New(16))
	want := legacyInventory("vps-1", splitSections(string(raw)), now, prev, hostMem)
	got := assemble.Build(c.inventoryInput(splitSections(string(raw)), now, prev, hostMem))

	if len(want.projects) < 5 || len(want.ingress) < 2 || len(want.edges) < 4 || len(want.containers) < 3 {
		t.Fatalf("fixture too thin to prove parity: %d projects, %d routes, %d edges, %d containers",
			len(want.projects), len(want.ingress), len(want.edges), len(want.containers))
	}
	equalLists(t, "projects", got.Projects, want.projects, func(p *asterismv1.Project) string { return p.Id })
	equalLists(t, "ports", got.Ports, want.ports, func(p *asterismv1.PortBinding) string { return p.Id })
	equalLists(t, "ingress", got.Ingress, want.ingress, func(r *asterismv1.IngressRoute) string { return r.Id })
	equalLists(t, "edges", got.Edges, want.edges, func(e *asterismv1.TopologyEdge) string { return e.Id })
	equalLists(t, "containers", got.Containers, want.containers, func(c *asterismv1.Container) string { return c.Id })
	if len(got.Events) != len(want.events) {
		t.Fatalf("events: got %d, want %d", len(got.Events), len(want.events))
	}
	for i := range want.events {
		if !proto.Equal(got.Events[i], want.events[i]) {
			t.Errorf("event %d differs:\n got  %v\n want %v", i, got.Events[i], want.events[i])
		}
	}
	if len(got.Workloads) != len(want.workloads) {
		t.Fatalf("workloads: got %d, want %d", len(got.Workloads), len(want.workloads))
	}
	for id, w := range want.workloads {
		if !proto.Equal(got.Workloads[id], w) {
			t.Errorf("workload %s differs:\n got  %v\n want %v", id, got.Workloads[id], w)
		}
	}
	// Order guarantees kept from the legacy code: ports by exposure then port,
	// routes by first hostname, projects by category order.
	for i := 1; i < len(got.Ports); i++ {
		a, b := got.Ports[i-1], got.Ports[i]
		if a.Exposure > b.Exposure || (a.Exposure == b.Exposure && a.HostPort > b.HostPort) {
			t.Fatalf("ports not sorted at %d", i)
		}
	}
	if got.Projects[0].Category != "infra" {
		t.Fatalf("first project category = %q, want infra", got.Projects[0].Category)
	}
}

func TestApplyInventoryStoresAssembledTopology(t *testing.T) {
	raw, err := os.ReadFile("testdata/inventory.txt")
	if err != nil {
		t.Fatal(err)
	}
	c := New(slog.New(slog.NewTextHandler(io.Discard, nil)), "vps-1", bus.New(1), ring.New(16))
	c.applyInventory(splitSections(string(raw)), time.Now())
	if h := c.HostSnapshot(); h.Hostname != "vps-1" || h.Cores != 4 || h.CpuModel != "AMD EPYC 7B13" {
		t.Fatalf("host facts: %v", h)
	}
	if _, ok := c.Workload(assemble.NginxUnitID); !ok {
		t.Fatal("nginx unit missing")
	}
	var routes []string
	for _, r := range c.IngressRoutes() {
		routes = append(routes, r.Id)
	}
	if strings.Join(routes, ",") != "ingress:nginx/legacy.example.com,ingress:nginx/shop.example.com,ingress:nginx/staging.example.com" {
		t.Fatalf("routes = %v", routes)
	}
	if len(c.DockerSnapshot().Containers) == 0 {
		t.Fatal("inspected containers not stored")
	}
}
