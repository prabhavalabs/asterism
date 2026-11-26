package sshbridge

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/bus"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

const (
	testFullID  = "3f2a9c1b7d4e0000000000000000000000000000000000000000000000000000"
	testShortID = "3f2a9c1b7d4e"
	testName    = "shop-api-1"
	testWlID    = "ctr:" + testName
)

// newSeededCollector builds a collector whose state looks like the result of
// one inventory poll, without needing SSH.
func newSeededCollector(t *testing.T) *Collector {
	t.Helper()
	c := New(slog.New(slog.NewTextHandler(io.Discard, nil)), "test-host", bus.New(16), ring.New(256))
	pb := &asterismv1.PortBinding{Id: "port:8080", WorkloadId: testWlID, HostIp: "127.0.0.1", HostPort: 8080, ContainerPort: 8080, Proto: "tcp"}
	wl := &asterismv1.Workload{
		Id: testWlID, Kind: asterismv1.WorkloadKind_WORKLOAD_KIND_CONTAINER, Name: testName,
		ProjectId: "proj:shop", ServiceName: "api", State: "running",
		Health: asterismv1.HealthStatus_HEALTH_STATUS_HEALTHY, Replicas: 1,
		Ports: []*asterismv1.PortBinding{pb}, Labels: map[string]string{"asterism.category": "production"},
	}
	c.workloads = map[string]*asterismv1.Workload{wl.Id: wl}
	c.projects = []*asterismv1.Project{{Id: "proj:shop", Name: "shop", Category: "production", Workloads: []*asterismv1.Workload{wl}}}
	c.ports = []*asterismv1.PortBinding{pb}
	c.ingress = []*asterismv1.IngressRoute{{Id: "ingress:nginx/shop.example.com", Hostnames: []string{"shop.example.com"}, Upstreams: []*asterismv1.UpstreamTarget{{WorkloadId: testWlID, Address: "127.0.0.1:8080"}}}}
	c.edges = []*asterismv1.TopologyEdge{{Id: "edge:nginx", SourceId: "unit:nginx.service", TargetId: testWlID, Label: "shop"}}
	c.events = []*asterismv1.Event{{Id: "evt:ssh:sync", EntityId: "host", Message: "synced"}}
	c.containers = []*asterismv1.Container{{
		Id: testWlID, ContainerId: testShortID, Name: testName, Pid: 4242,
		State: asterismv1.ContainerState_CONTAINER_STATE_RUNNING, Status: "Up 5 minutes",
	}}
	c.dockerSyncMs = time.Now().UnixMilli()
	return c
}

func fastSections(i int) map[string]string {
	return map[string]string{
		"STAT":   fmt.Sprintf("cpu  %d 0 %d %d 0 0 0 0 0 0", 1000+i*7, 500+i*3, 10000+i*11),
		"LOAD":   "0.42 0.38 0.30 1/234 5678",
		"MEM":    "MemTotal:        8000000 kB\nMemAvailable:    4000000 kB\nSwapTotal:       0 kB\nSwapFree:        0 kB",
		"DISK":   "/dev/sda1 100000000000 40000000000 60000000000 40% /",
		"UPTIME": fmt.Sprintf("%d.00 1000.00", 3600+i),
		"NET":    fmt.Sprintf("Inter-|\n face |\n  eth0: %d 0 0 0 0 0 0 0 %d 0 0 0 0 0 0 0", 1_000_000+i*1000, 2_000_000+i*2000),
		"DSTATS": fmt.Sprintf("%s|%s|%d.50%%|%dMiB / 1GiB|%dkB / %dkB|1MB / 2MB|%d", testFullID, testName, i%90, 100+i%200, 10+i, 20+i, 5+i%10),
	}
}

func updatedContainer(i int) *asterismv1.Container {
	c := &asterismv1.Container{Id: testWlID, ContainerId: testShortID, Name: testName, StartedAtMs: int64(i), RestartCount: uint32(i)}
	if i%2 == 0 {
		c.State, c.Status = asterismv1.ContainerState_CONTAINER_STATE_EXITED, "Exited (0) 1 second ago"
	} else {
		c.State, c.Status = asterismv1.ContainerState_CONTAINER_STATE_RUNNING, "Up 1 second"
	}
	return c
}

func readEverything(t *testing.T, c *Collector) {
	t.Helper()
	var msgs []proto.Message
	for _, proj := range c.Projects() {
		msgs = append(msgs, proj)
		for _, w := range proj.Workloads {
			w.CpuUtil = -1
		}
		proj.MemUsedBytes = 0
	}
	for _, w := range c.Workloads() {
		msgs = append(msgs, w)
		w.State = "scribbled"
	}
	if w, ok := c.Workload(testWlID); ok {
		msgs = append(msgs, w)
	}
	for _, pb := range c.Ports() {
		msgs = append(msgs, pb)
	}
	for _, r := range c.IngressRoutes() {
		msgs = append(msgs, r)
	}
	for _, e := range c.Edges() {
		msgs = append(msgs, e)
	}
	for _, e := range c.Events() {
		msgs = append(msgs, e)
	}
	msgs = append(msgs, c.HostSnapshot())
	for _, ctr := range c.DockerSnapshot().Containers {
		msgs = append(msgs, ctr)
		ctr.Protected = true
	}
	for _, m := range msgs {
		if _, err := proto.Marshal(m); err != nil {
			t.Errorf("marshal %T: %v", m, err)
			return
		}
	}
}

// TestConcurrentReadersDuringPollAndActions fails under -race if any getter
// hands out a pointer that the fast-metrics poll or a container action later
// mutates.
func TestConcurrentReadersDuringPollAndActions(t *testing.T) {
	c := newSeededCollector(t)

	const (
		polls   = 400
		actions = 400
		readers = 4
	)
	writersDone := make(chan struct{})
	var writers, readersWG sync.WaitGroup

	writers.Add(2)
	go func() {
		defer writers.Done()
		base := time.Now()
		idByPid := map[int32]string{4242: testShortID}
		for i := 0; i < polls; i++ {
			c.applyFastMetrics(fastSections(i), idByPid, base.Add(time.Duration(i)*5*time.Second))
		}
	}()
	go func() {
		defer writers.Done()
		for i := 0; i < actions; i++ {
			got := c.applyContainerUpdate(updatedContainer(i))
			got.State = asterismv1.ContainerState_CONTAINER_STATE_DEAD // caller-owned
		}
	}()

	for r := 0; r < readers; r++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for {
				readEverything(t, c)
				select {
				case <-writersDone:
					return
				default:
				}
			}
		}()
	}

	writers.Wait()
	close(writersDone)
	readersWG.Wait()

	w, ok := c.Workload(testWlID)
	if !ok {
		t.Fatalf("workload %s missing", testWlID)
	}
	if w.State == "scribbled" || w.CpuUtil < 0 {
		t.Fatalf("reader mutation leaked into workload: state=%q cpu=%v", w.State, w.CpuUtil)
	}
	for _, ctr := range c.DockerSnapshot().Containers {
		if ctr.State == asterismv1.ContainerState_CONTAINER_STATE_DEAD || ctr.Protected {
			t.Fatalf("caller mutation leaked into cached container %s", ctr.Name)
		}
	}
}

// TestGettersReturnCallerOwnedCopies is the deterministic counterpart: it
// fails without -race when getters share internal pointers.
func TestGettersReturnCallerOwnedCopies(t *testing.T) {
	c := newSeededCollector(t)

	w, _ := c.Workload(testWlID)
	w.Name = "mutated"
	w.Ports[0].HostPort = 1
	w.Labels["asterism.category"] = "mutated"
	c.Projects()[0].Workloads[0].Image = "mutated"
	c.Workloads()[0].ServiceName = "mutated"
	c.Ports()[0].HostIp = "mutated"
	c.IngressRoutes()[0].Upstreams[0].Address = "mutated"
	c.Edges()[0].Label = "mutated"
	c.Events()[0].Message = "mutated"

	got, _ := c.Workload(testWlID)
	if got.Name != testName || got.Ports[0].HostPort != 8080 || got.Labels["asterism.category"] != "production" || got.Image == "mutated" || got.ServiceName == "mutated" {
		t.Errorf("workload getters share state: %v", got)
	}
	if c.Ports()[0].HostIp == "mutated" {
		t.Error("Ports() shares state")
	}
	if c.IngressRoutes()[0].Upstreams[0].Address == "mutated" {
		t.Error("IngressRoutes() shares state")
	}
	if c.Edges()[0].Label == "mutated" {
		t.Error("Edges() shares state")
	}
	if c.Events()[0].Message == "mutated" {
		t.Error("Events() shares state")
	}

	empty := New(slog.New(slog.NewTextHandler(io.Discard, nil)), "x", bus.New(1), ring.New(1))
	if empty.Projects() == nil || empty.Ports() == nil || empty.IngressRoutes() == nil || empty.Edges() == nil || empty.Events() == nil {
		t.Error("getters on an empty collector must return non-nil slices")
	}
}

// TestApplyFastMetricsUpdatesWorkload guards the extraction of the poll's
// apply step: live stats must still reach the workload and project rollup.
func TestApplyFastMetricsUpdatesWorkload(t *testing.T) {
	c := newSeededCollector(t)
	c.applyFastMetrics(fastSections(10), map[int32]string{4242: testShortID}, time.Now())

	w, _ := c.Workload(testWlID)
	if w.CpuUtil != 0.105 || w.MemUsedBytes != 110*1024*1024 || w.MemLimitBytes != 1024*1024*1024 {
		t.Fatalf("workload stats not applied: cpu=%v mem=%d limit=%d", w.CpuUtil, w.MemUsedBytes, w.MemLimitBytes)
	}
	if p := c.Projects()[0]; p.MemUsedBytes != w.MemUsedBytes {
		t.Fatalf("project rollup mem=%d, want %d", p.MemUsedBytes, w.MemUsedBytes)
	}
	if h := c.HostSnapshot(); h.MemTotalBytes != 8000000*1024 || h.UptimeSeconds != 3610 {
		t.Fatalf("host vitals not applied: mem=%d uptime=%d", h.MemTotalBytes, h.UptimeSeconds)
	}
}
