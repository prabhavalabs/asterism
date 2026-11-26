package service

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/dockercli"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/store/ring"
	"google.golang.org/protobuf/proto"
)

// listInventory is a full InventoryProvider fixture for the paged lists.
type listInventory struct {
	projects []*asterismv1.Project
	ports    []*asterismv1.PortBinding
	ingress  []*asterismv1.IngressRoute
	events   []*asterismv1.Event
	logs     map[string][]*asterismv1.LogLine
}

func (f *listInventory) Projects() []*asterismv1.Project { return dockercli.CloneAll(f.projects) }
func (f *listInventory) Workloads() []*asterismv1.Workload {
	var out []*asterismv1.Workload
	for _, p := range f.projects {
		out = append(out, dockercli.CloneAll(p.Workloads)...)
	}
	return out
}
func (f *listInventory) Workload(string) (*asterismv1.Workload, bool) { return nil, false }
func (f *listInventory) Ports() []*asterismv1.PortBinding             { return dockercli.CloneAll(f.ports) }
func (f *listInventory) IngressRoutes() []*asterismv1.IngressRoute {
	return dockercli.CloneAll(f.ingress)
}
func (f *listInventory) Edges() []*asterismv1.TopologyEdge    { return nil }
func (f *listInventory) Events() []*asterismv1.Event          { return dockercli.CloneAll(f.events) }
func (f *listInventory) Logs(id string) []*asterismv1.LogLine { return dockercli.CloneAll(f.logs[id]) }

// snapDocker serves a fixed Docker snapshot.
type snapDocker struct{ snap dockercli.Snapshot }

func (s *snapDocker) DockerSnapshot() dockercli.Snapshot {
	return dockercli.Snapshot{
		Engine:        proto.Clone(s.snap.Engine).(*asterismv1.DockerEngine),
		Containers:    dockercli.CloneAll(s.snap.Containers),
		Images:        dockercli.CloneAll(s.snap.Images),
		Volumes:       dockercli.CloneAll(s.snap.Volumes),
		Networks:      dockercli.CloneAll(s.snap.Networks),
		CollectedAtMs: 42,
	}
}

func newListFixture() *Observatory {
	wl := func(id, proj string, role asterismv1.WorkloadRole, mem uint64) *asterismv1.Workload {
		return &asterismv1.Workload{Id: "ctr:" + id, Name: id, ProjectId: proj, Role: role, MemUsedBytes: mem, Image: id + ":1", State: "running"}
	}
	app, db := asterismv1.WorkloadRole_WORKLOAD_ROLE_APP, asterismv1.WorkloadRole_WORKLOAD_ROLE_DATABASE
	inv := &listInventory{
		projects: []*asterismv1.Project{
			{Id: "proj:shop", Name: "shop", Category: "production", MemUsedBytes: 300, Workloads: []*asterismv1.Workload{wl("shop-api", "proj:shop", app, 200), wl("shop-db", "proj:shop", db, 100)}},
			{Id: "proj:blog", Name: "blog", Category: "production", MemUsedBytes: 300, Workloads: []*asterismv1.Workload{wl("blog-web", "proj:blog", app, 300)}},
			{Id: "proj:lab", Name: "lab", Category: "staging", MemUsedBytes: 50, Workloads: []*asterismv1.Workload{wl("lab-db", "proj:lab", db, 50)}},
			{Id: "proj:edge", Name: "edge", Category: "infra", MemUsedBytes: 900},
		},
		ports: []*asterismv1.PortBinding{
			{Id: "port:5432", WorkloadName: "shop-db", ContainerPort: 5432, HostPort: 5432, HostIp: "127.0.0.1", Exposure: asterismv1.PortExposure_PORT_EXPOSURE_LOOPBACK, Proto: "tcp"},
			{Id: "port:443", WorkloadName: "nginx", HostPort: 443, ContainerPort: 443, HostIp: "0.0.0.0", Exposure: asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC, Proto: "tcp"},
			{Id: "port:3000", WorkloadName: "blog-web", ContainerPort: 3000, Exposure: asterismv1.PortExposure_PORT_EXPOSURE_INTERNAL, Proto: "tcp"},
			{Id: "port:80", WorkloadName: "nginx", HostPort: 80, ContainerPort: 80, HostIp: "0.0.0.0", Exposure: asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC, Proto: "tcp"},
			{Id: "port:22", WorkloadName: "sshd", HostPort: 22, ContainerPort: 22, HostIp: "0.0.0.0", Exposure: asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC, Proto: "tcp"},
		},
		ingress: []*asterismv1.IngressRoute{
			{Id: "ingress:caddy/b", Hostnames: []string{"blog.example.com"}, ListenPort: 443, Upstreams: []*asterismv1.UpstreamTarget{{Address: "blog-web:3000"}}},
			{Id: "ingress:nginx/a", Hostnames: []string{"api.example.com"}, ListenPort: 443, Upstreams: []*asterismv1.UpstreamTarget{{Address: "shop-api:8080"}, {Address: "shop-api:8081"}}},
		},
		logs: map[string][]*asterismv1.LogLine{},
	}
	for i := 0; i < 30; i++ {
		sev := []string{"info", "warning", "critical", "error", "info"}[i%5]
		kind := []string{"container.start", "audit.container.stop", "alert.memory"}[i%3]
		ent := []string{"ctr:shop-api", "ctr:blog-web"}[i%2]
		inv.events = append(inv.events, &asterismv1.Event{
			Id: fmt.Sprintf("evt-%02d", i), TsMs: int64(1000 + i/2), EntityId: ent, EntityName: strings.TrimPrefix(ent, "ctr:"),
			Kind: kind, Severity: sev, Message: fmt.Sprintf("event %d", i),
		})
	}
	o := New(bus.New(8), ring.New(10), nil, inv, "showcase")
	running, exited := asterismv1.ContainerState_CONTAINER_STATE_RUNNING, asterismv1.ContainerState_CONTAINER_STATE_EXITED
	ctr := func(name, proj string, st asterismv1.ContainerState, mem uint64) *asterismv1.Container {
		c := &asterismv1.Container{Id: "ctr:" + name, Name: name, ContainerId: fmt.Sprintf("%012x", len(name)*7919), ProjectId: proj, State: st, Image: name + ":1"}
		if st == running {
			c.Stats = &asterismv1.ContainerStats{MemUsedBytes: mem}
		}
		return c
	}
	cs := []*asterismv1.Container{
		ctr("shop-api", "proj:shop", running, 200), ctr("shop-db", "proj:shop", running, 100),
		ctr("shop-migrate", "proj:shop", exited, 0), ctr("blog-web", "proj:blog", running, 300),
		ctr("lab-db", "proj:lab", exited, 0),
	}
	cs[2].OomKilled = true
	cs[3].Health = asterismv1.ContainerHealth_CONTAINER_HEALTH_UNHEALTHY
	cs[1].Ports = []*asterismv1.ContainerPort{{ContainerPort: 5432, HostPort: 15432}}
	img := func(id, repo string, size uint64, n uint32, dangling bool) *asterismv1.DockerImage {
		return &asterismv1.DockerImage{Id: id, Repository: repo, Tag: "1", RepoTags: []string{repo + ":1"}, SizeBytes: size, Containers: n, Dangling: dangling}
	}
	vol := func(name, proj string, size int64, n uint32) *asterismv1.DockerVolume {
		return &asterismv1.DockerVolume{Name: name, ComposeProject: proj, SizeBytes: size, Containers: n, Anonymous: len(name) == 64}
	}
	o.ConfigureDocker(&snapDocker{snap: dockercli.Snapshot{
		Engine:     &asterismv1.DockerEngine{Available: true},
		Containers: cs,
		Images: []*asterismv1.DockerImage{
			img("sha256:a", "postgres", 300, 2, false), img("sha256:b", "<none>", 300, 0, true),
			img("sha256:c", "node", 100, 0, false), img("sha256:d", "caddy", 50, 1, false),
		},
		Volumes: []*asterismv1.DockerVolume{
			vol("shop_pg", "shop", 900, 1), vol("blog_up", "blog", -1, 0), vol(strings.Repeat("f", 64), "", 10, 0),
		},
		Networks: []*asterismv1.DockerNetwork{
			{Id: "n3", Name: "shop_net", Driver: "bridge", Containers: []*asterismv1.NetworkAttachment{{Name: "shop-api"}, {Name: "shop-db"}}},
			{Id: "n1", Name: "bridge", Driver: "bridge"},
			{Id: "n2", Name: "host", Driver: "host"},
		},
	}}, nil, ActionPolicy{})
	return o
}

func TestListProjectsPage(t *testing.T) {
	o := newListFixture()
	tests := []struct {
		name    string
		req     *asterismv1.ListProjectsRequest
		want    []string
		facets  []string
		total   int32
		wantErr bool
	}{
		{"default memory desc with id tie-break", &asterismv1.ListProjectsRequest{},
			[]string{"edge", "blog", "shop", "lab"}, []string{"infra=1", "production=2", "staging=1"}, 4, false},
		{"category filter keeps category facet unfiltered", &asterismv1.ListProjectsRequest{Category: "production"},
			[]string{"blog", "shop"}, []string{"infra=1", "production=2", "staging=1"}, 2, false},
		{"query narrows facets", &asterismv1.ListProjectsRequest{Query: "SHO"},
			[]string{"shop"}, []string{"production=1"}, 1, false},
		{"order by name", &asterismv1.ListProjectsRequest{OrderBy: "name"},
			[]string{"blog", "edge", "lab", "shop"}, nil, 4, false},
		{"page two", &asterismv1.ListProjectsRequest{OrderBy: "name", Page: &asterismv1.PageRequest{PageSize: 1, Page: 2}},
			[]string{}, nil, 4, false}, // page size clamps to 10, so page 2 is past the end
		{"unknown order field", &asterismv1.ListProjectsRequest{OrderBy: "size"}, nil, nil, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := o.ListProjectsPage(tt.req)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidArgument) || !strings.Contains(err.Error(), "allowed: name, category, memory, cpu, workloads, health") {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, p := range resp.Projects {
				names = append(names, p.Name)
			}
			if !slices.Equal(names, tt.want) && !(len(names) == 0 && len(tt.want) == 0) {
				t.Fatalf("projects = %v, want %v", names, tt.want)
			}
			if resp.PageInfo.TotalSize != tt.total {
				t.Fatalf("total = %d, want %d", resp.PageInfo.TotalSize, tt.total)
			}
			if tt.facets != nil {
				if got := facetStrings(resp.CategoryCounts); !slices.Equal(got, tt.facets) {
					t.Fatalf("category facets = %v, want %v", got, tt.facets)
				}
			}
		})
	}
}

func TestListWorkloadsPage(t *testing.T) {
	o := newListFixture()
	resp, err := o.ListWorkloadsPage(&asterismv1.ListWorkloadsRequest{Role: asterismv1.WorkloadRole_WORKLOAD_ROLE_DATABASE})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, w := range resp.Workloads {
		ids = append(ids, w.Id)
	}
	if !slices.Equal(ids, []string{"ctr:lab-db", "ctr:shop-db"}) {
		t.Fatalf("workloads = %v", ids)
	}
	if len(resp.RoleCounts) != 2 || resp.RoleCounts[0].Count != 2 || resp.RoleCounts[1].Count != 2 {
		t.Fatalf("role counts must ignore the role filter: %v", resp.RoleCounts)
	}
	if got := facetStrings(resp.ProjectCounts); !slices.Equal(got, []string{"proj:lab=1", "proj:shop=1"}) {
		t.Fatalf("project counts = %v", got)
	}
	resp, _ = o.ListWorkloadsPage(&asterismv1.ListWorkloadsRequest{OrderBy: "memory desc", Query: "db"})
	if len(resp.Workloads) != 2 || resp.Workloads[0].Name != "shop-db" {
		t.Fatalf("memory order = %v", resp.Workloads)
	}
}

func TestListPortsPage(t *testing.T) {
	o := newListFixture()
	tests := []struct {
		name   string
		req    *asterismv1.ListPortsRequest
		want   string
		counts [4]uint32 // total, public, loopback, internal
	}{
		{"default exposure then port", &asterismv1.ListPortsRequest{}, "port:22,port:80,port:443,port:5432,port:3000", [4]uint32{5, 3, 1, 1}},
		{"exposure filter keeps exposure facet", &asterismv1.ListPortsRequest{Exposure: asterismv1.PortExposure_PORT_EXPOSURE_PUBLIC}, "port:22,port:80,port:443", [4]uint32{5, 3, 1, 1}},
		{"numeric query matches the port exactly", &asterismv1.ListPortsRequest{Query: "443"}, "port:443", [4]uint32{1, 1, 0, 0}},
		{"text query", &asterismv1.ListPortsRequest{Query: "NGINX"}, "port:80,port:443", [4]uint32{2, 2, 0, 0}},
		{"order by port desc", &asterismv1.ListPortsRequest{OrderBy: "port desc"}, "port:5432,port:3000,port:443,port:80,port:22", [4]uint32{5, 3, 1, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := o.ListPortsPage(tt.req)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, p := range resp.Ports {
				ids = append(ids, p.Id)
			}
			if got := strings.Join(ids, ","); got != tt.want {
				t.Fatalf("ports = %s, want %s", got, tt.want)
			}
			c := resp.ExposureCounts
			if got := [4]uint32{c.Total, c.Public, c.Loopback, c.Internal}; got != tt.counts {
				t.Fatalf("exposure counts = %v, want %v", got, tt.counts)
			}
		})
	}
}

func TestListIngressPage(t *testing.T) {
	o := newListFixture()
	resp, err := o.ListIngressPage(&asterismv1.ListIngressRequest{})
	if err != nil || len(resp.Routes) != 2 || resp.Routes[0].Id != "ingress:nginx/a" {
		t.Fatalf("default hostname order: %v %v", resp, err)
	}
	resp, _ = o.ListIngressPage(&asterismv1.ListIngressRequest{Query: "blog-web:3000"})
	if len(resp.Routes) != 1 || resp.PageInfo.TotalSize != 1 {
		t.Fatalf("upstream query: %v", resp.Routes)
	}
	resp, _ = o.ListIngressPage(&asterismv1.ListIngressRequest{OrderBy: "upstreams desc"})
	if resp.Routes[0].Id != "ingress:nginx/a" {
		t.Fatalf("upstreams order: %v", resp.Routes)
	}
}

func TestListEventsPage(t *testing.T) {
	o := newListFixture()
	tests := []struct {
		name    string
		req     *asterismv1.ListEventsRequest
		total   int32
		first   string
		sev     [4]uint32 // total, info, warning, critical
		kinds   [4]uint32 // total, lifecycle, audit, other
		wantErr string
	}{
		{"newest first with id tie-break", &asterismv1.ListEventsRequest{Page: &asterismv1.PageRequest{PageSize: 10}},
			30, "evt-28", [4]uint32{30, 12, 6, 12}, [4]uint32{30, 10, 10, 10}, ""},
		{"severity filter ignores its own facet", &asterismv1.ListEventsRequest{Severities: []string{"critical"}},
			12, "evt-28", [4]uint32{30, 12, 6, 12}, [4]uint32{12, 4, 4, 4}, ""},
		{"kind group filter ignores its own facet", &asterismv1.ListEventsRequest{KindGroup: "audit"},
			10, "evt-28", [4]uint32{10, 4, 2, 4}, [4]uint32{30, 10, 10, 10}, ""},
		{"alerts is an alias of other", &asterismv1.ListEventsRequest{KindGroup: "alerts"},
			10, "evt-29", [4]uint32{10, 4, 2, 4}, [4]uint32{30, 10, 10, 10}, ""},
		{"entity scope", &asterismv1.ListEventsRequest{EntityId: "ctr:blog-web"},
			15, "evt-29", [4]uint32{15, 6, 3, 6}, [4]uint32{15, 5, 5, 5}, ""},
		{"legacy limit", &asterismv1.ListEventsRequest{Limit: 12},
			30, "evt-28", [4]uint32{30, 12, 6, 12}, [4]uint32{30, 10, 10, 10}, ""},
		{"oldest first", &asterismv1.ListEventsRequest{OrderBy: "ts"},
			30, "evt-00", [4]uint32{30, 12, 6, 12}, [4]uint32{30, 10, 10, 10}, ""},
		{"bad severity", &asterismv1.ListEventsRequest{Severities: []string{"loud"}}, 0, "", [4]uint32{}, [4]uint32{}, "unknown severity"},
		{"bad kind group", &asterismv1.ListEventsRequest{KindGroup: "misc"}, 0, "", [4]uint32{}, [4]uint32{}, "unknown kind_group"},
		{"bad order", &asterismv1.ListEventsRequest{OrderBy: "time desc"}, 0, "", [4]uint32{}, [4]uint32{}, "allowed: ts, severity, kind, entity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := o.ListEventsPage(tt.req)
			if tt.wantErr != "" {
				if !errors.Is(err, ErrInvalidArgument) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resp.PageInfo.TotalSize != tt.total || resp.Events[0].Id != tt.first {
				t.Fatalf("total=%d first=%s, want %d %s", resp.PageInfo.TotalSize, resp.Events[0].Id, tt.total, tt.first)
			}
			s, k := resp.SeverityCounts, resp.KindCounts
			if got := [4]uint32{s.Total, s.Info, s.Warning, s.Critical}; got != tt.sev {
				t.Fatalf("severity counts = %v, want %v", got, tt.sev)
			}
			if got := [4]uint32{k.Total, k.Lifecycle, k.Audit, k.Other}; got != tt.kinds {
				t.Fatalf("kind counts = %v, want %v", got, tt.kinds)
			}
		})
	}
	// Legacy limit caps the page; numbered pages never overlap.
	resp, _ := o.ListEventsPage(&asterismv1.ListEventsRequest{Limit: 12})
	if len(resp.Events) != 12 || resp.PageInfo.PageSize != 12 {
		t.Fatalf("legacy limit page = %d/%d", len(resp.Events), resp.PageInfo.PageSize)
	}
	seen := map[string]bool{}
	for page := int32(1); page <= 3; page++ {
		resp, _ := o.ListEventsPage(&asterismv1.ListEventsRequest{Page: &asterismv1.PageRequest{PageSize: 10, Page: page}})
		for _, e := range resp.Events {
			if seen[e.Id] {
				t.Fatalf("event %s on two pages", e.Id)
			}
			seen[e.Id] = true
		}
	}
	if len(seen) != 30 {
		t.Fatalf("pages covered %d events, want 30", len(seen))
	}
}

func TestTailLogsCursor(t *testing.T) {
	o := newListFixture()
	inv := o.inventory.(*listInventory)
	// ts: 1..100, plus a run of five lines sharing ts 50.
	var lines []*asterismv1.LogLine
	for i := 1; i <= 100; i++ {
		lines = append(lines, &asterismv1.LogLine{TsMs: int64(i), Text: fmt.Sprintf("line %d", i)})
		if i == 50 {
			for j := 0; j < 4; j++ {
				lines = append(lines, &asterismv1.LogLine{TsMs: 50, Text: fmt.Sprintf("dup %d", j)})
			}
		}
	}
	inv.logs["ctr:x"] = lines

	resp := o.TailLogs(&asterismv1.TailLogsRequest{WorkloadId: "ctr:x", Limit: 30})
	if len(resp.Lines) != 30 || resp.Lines[0].TsMs != 71 || resp.Lines[29].TsMs != 100 || !resp.HasMore || resp.NextBeforeTsMs != 71 {
		t.Fatalf("newest page: n=%d first=%d more=%v next=%d", len(resp.Lines), resp.Lines[0].TsMs, resp.HasMore, resp.NextBeforeTsMs)
	}
	// Walk every page with the cursor: each line exactly once, no split ts group.
	total, before, pages := 0, int64(0), 0
	for {
		resp := o.TailLogs(&asterismv1.TailLogsRequest{WorkloadId: "ctr:x", Limit: 23, BeforeTsMs: before})
		total += len(resp.Lines)
		pages++
		if !slices.IsSortedFunc(resp.Lines, func(a, b *asterismv1.LogLine) int { return int(a.TsMs - b.TsMs) }) {
			t.Fatal("page not oldest-first")
		}
		if !resp.HasMore {
			if resp.NextBeforeTsMs != 0 {
				t.Fatal("cursor set on last page")
			}
			break
		}
		before = resp.NextBeforeTsMs
		if pages > 20 {
			t.Fatal("cursor did not terminate")
		}
	}
	if total != len(lines) {
		t.Fatalf("cursor walk returned %d lines, want %d", total, len(lines))
	}

	tests := []struct {
		name      string
		req       *asterismv1.TailLogsRequest
		wantN     int
		wantFirst int64
		wantMore  bool
	}{
		{"default limit returns everything here", &asterismv1.TailLogsRequest{WorkloadId: "ctr:x"}, 104, 1, false},
		{"limit is capped at 500", &asterismv1.TailLogsRequest{WorkloadId: "ctr:x", Limit: 9000}, 104, 1, false},
		{"grep then cursor", &asterismv1.TailLogsRequest{WorkloadId: "ctr:x", Grep: "DUP", Limit: 2}, 4, 50, false}, // one ts group larger than the limit is kept whole
		{"page boundary inside a ts group shrinks", &asterismv1.TailLogsRequest{WorkloadId: "ctr:x", BeforeTsMs: 52, Limit: 4}, 1, 51, true},
		{"cursor before everything", &asterismv1.TailLogsRequest{WorkloadId: "ctr:x", BeforeTsMs: 1}, 0, 0, false},
		{"unknown workload", &asterismv1.TailLogsRequest{WorkloadId: "ctr:none"}, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := o.TailLogs(tt.req)
			if resp.Lines == nil || len(resp.Lines) != tt.wantN || resp.HasMore != tt.wantMore {
				t.Fatalf("n=%d more=%v, want %d %v", len(resp.Lines), resp.HasMore, tt.wantN, tt.wantMore)
			}
			if tt.wantN > 0 && resp.Lines[0].TsMs != tt.wantFirst {
				t.Fatalf("first ts = %d, want %d", resp.Lines[0].TsMs, tt.wantFirst)
			}
		})
	}
}

func TestListContainersFacetsAndPaging(t *testing.T) {
	o := newListFixture()
	running := asterismv1.ContainerState_CONTAINER_STATE_RUNNING
	tests := []struct {
		name     string
		req      *asterismv1.ListContainersRequest
		want     string
		total    uint32 // counts.Total (state facet)
		running  uint32
		attn     uint32
		projects []string
		mem      uint64
	}{
		{"default project then name", &asterismv1.ListContainersRequest{},
			"blog-web,lab-db,shop-api,shop-db,shop-migrate", 5, 3, 2, []string{"proj:blog=1", "proj:lab=1", "proj:shop=3"}, 600},
		{"state filter keeps state counts over the project", &asterismv1.ListContainersRequest{ProjectId: "proj:shop", States: []asterismv1.ContainerState{running}},
			"shop-api,shop-db", 3, 2, 1, []string{"proj:blog=1", "proj:shop=2"}, 300},
		{"attention only", &asterismv1.ListContainersRequest{AttentionOnly: true},
			"blog-web,shop-migrate", 5, 3, 2, []string{"proj:blog=1", "proj:shop=1"}, 300},
		{"port query", &asterismv1.ListContainersRequest{Query: "15432"},
			"shop-db", 1, 1, 0, []string{"proj:shop=1"}, 100},
		{"memory desc", &asterismv1.ListContainersRequest{OrderBy: "memory desc"},
			"blog-web,shop-api,shop-db,lab-db,shop-migrate", 5, 3, 2, nil, 600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := o.ListContainers(tt.req)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, c := range resp.Containers {
				names = append(names, c.Name)
			}
			if got := strings.Join(names, ","); got != tt.want {
				t.Fatalf("containers = %s, want %s", got, tt.want)
			}
			if resp.Counts.Total != tt.total || resp.Counts.Running != tt.running || resp.Counts.Attention != tt.attn {
				t.Fatalf("counts = %+v", resp.Counts)
			}
			if tt.projects != nil && !slices.Equal(facetStrings(resp.ProjectCounts), tt.projects) {
				t.Fatalf("project counts = %v, want %v", facetStrings(resp.ProjectCounts), tt.projects)
			}
			if resp.MemUsedBytes != tt.mem {
				t.Fatalf("mem = %d, want %d", resp.MemUsedBytes, tt.mem)
			}
		})
	}
	resp, _ := o.ListContainers(&asterismv1.ListContainersRequest{Page: &asterismv1.PageRequest{PageSize: 10, Page: 2}})
	if len(resp.Containers) != 0 || resp.PageInfo.TotalSize != 5 || resp.PageInfo.TotalPages != 1 || resp.Counts.Total != 5 {
		t.Fatalf("past-the-end page: %+v %+v", resp.PageInfo, resp.Counts)
	}
	if _, err := o.ListContainers(&asterismv1.ListContainersRequest{OrderBy: "cpu sideways"}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("bad order: %v", err)
	}
}

func TestListImagesVolumesNetworks(t *testing.T) {
	o := newListFixture()

	imgs, err := o.ListImages(&asterismv1.ListImagesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, i := range imgs.Images {
		ids = append(ids, i.Id)
	}
	// size desc, tie on 300 broken by id.
	if got := strings.Join(ids, ","); got != "sha256:a,sha256:b,sha256:c,sha256:d" {
		t.Fatalf("images = %s", got)
	}
	if c := imgs.Counts; c.Total != 4 || c.InUse != 2 || c.Dangling != 1 || c.Unused != 2 || imgs.TotalSizeBytes != 750 {
		t.Fatalf("image counts = %+v size=%d", c, imgs.TotalSizeBytes)
	}
	imgs, _ = o.ListImages(&asterismv1.ListImagesRequest{InUseOnly: true, OrderBy: "repository"})
	if len(imgs.Images) != 2 || imgs.Images[0].Repository != "caddy" || imgs.Counts.Total != 4 || imgs.TotalSizeBytes != 350 {
		t.Fatalf("in-use images = %v counts=%+v", imgs.Images, imgs.Counts)
	}
	imgs, _ = o.ListImages(&asterismv1.ListImagesRequest{DanglingOnly: true})
	if len(imgs.Images) != 1 || !imgs.Images[0].Dangling {
		t.Fatalf("dangling images = %v", imgs.Images)
	}

	vols, err := o.ListVolumes(&asterismv1.ListVolumesRequest{UnusedOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(vols.Volumes) != 2 || vols.Volumes[0].Name != "blog_up" || vols.TotalSizeBytes != 10 {
		t.Fatalf("unused volumes = %v size=%d", vols.Volumes, vols.TotalSizeBytes)
	}
	if c := vols.Counts; c.Total != 3 || c.InUse != 1 || c.Unused != 2 || c.Anonymous != 1 {
		t.Fatalf("volume counts = %+v", c)
	}
	vols, _ = o.ListVolumes(&asterismv1.ListVolumesRequest{OrderBy: "size desc", Query: "_"})
	if len(vols.Volumes) != 2 || vols.Volumes[0].Name != "shop_pg" || vols.Counts.Total != 2 {
		t.Fatalf("volume query = %v", vols.Volumes)
	}

	nets, err := o.ListNetworks(&asterismv1.ListNetworksRequest{})
	if err != nil || len(nets.Networks) != 3 || nets.Networks[0].Name != "bridge" || nets.CollectedAtMs != 42 {
		t.Fatalf("networks = %v %v", nets, err)
	}
	nets, _ = o.ListNetworks(&asterismv1.ListNetworksRequest{Query: "shop-db"})
	if len(nets.Networks) != 1 || nets.Networks[0].Name != "shop_net" {
		t.Fatalf("network attachment query = %v", nets.Networks)
	}
	nets, _ = o.ListNetworks(&asterismv1.ListNetworksRequest{OrderBy: "containers desc"})
	if nets.Networks[0].Name != "shop_net" {
		t.Fatalf("networks by containers = %v", nets.Networks)
	}
	if _, err := o.ListNetworks(&asterismv1.ListNetworksRequest{OrderBy: "subnet"}); err == nil || !strings.Contains(err.Error(), "allowed: name, driver, containers, created") {
		t.Fatalf("bad network order: %v", err)
	}
}
