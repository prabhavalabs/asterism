package showcase

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/bus"
	"github.com/prabhavalabs/asterism/internal/collect/favicon"
	asterismv1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
	"github.com/prabhavalabs/asterism/internal/store/ring"
)

func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	p := New(bus.New(16), ring.New(256))
	p.actionDelay = 0
	return p
}

func containerID(t *testing.T, p *Provider, name string) string {
	t.Helper()
	for _, c := range p.DockerSnapshot().Containers {
		if c.Name == name {
			return c.ContainerId
		}
	}
	t.Fatalf("showcase container %q not found", name)
	return ""
}

// readEverything touches every field of every message the provider hands out
// (proto.Marshal walks them all) and then scribbles on the results the way a
// service layer that annotates responses would.
func readEverything(t *testing.T, p *Provider) {
	t.Helper()
	var msgs []proto.Message
	for _, proj := range p.Projects() {
		msgs = append(msgs, proj)
		for _, w := range proj.Workloads {
			w.CpuUtil = -1
		}
		proj.MemUsedBytes = 0
	}
	for _, w := range p.Workloads() {
		msgs = append(msgs, w)
		w.State = "scribbled"
	}
	if w, ok := p.Workload("ctr:nova-api"); ok {
		msgs = append(msgs, w)
		w.MemUsedBytes = 0
	}
	for _, pb := range p.Ports() {
		msgs = append(msgs, pb)
	}
	for _, r := range p.IngressRoutes() {
		msgs = append(msgs, r)
	}
	for _, e := range p.Edges() {
		msgs = append(msgs, e)
	}
	for _, e := range p.Events() {
		msgs = append(msgs, e)
	}
	for _, l := range p.Logs("ctr:nova-api") {
		msgs = append(msgs, l)
	}
	snap := p.DockerSnapshot()
	for _, c := range snap.Containers {
		msgs = append(msgs, c)
		c.Protected = true
	}
	for _, m := range msgs {
		if _, err := proto.Marshal(m); err != nil {
			t.Errorf("marshal %T: %v", m, err)
			return
		}
	}
}

// TestConcurrentReadersDuringStepAndActions fails under -race if any getter
// hands out a pointer that step() or ContainerAction later mutates.
func TestConcurrentReadersDuringStepAndActions(t *testing.T) {
	p := newTestProvider(t)
	apiID := containerID(t, p, "nova-api")
	workerID := containerID(t, p, "nova-worker")

	const (
		steps       = 300
		actionLoops = 25
		readers     = 4
	)
	writersDone := make(chan struct{})
	var writers, readersWG sync.WaitGroup

	writers.Add(2)
	go func() {
		defer writers.Done()
		base := time.Now()
		for i := 0; i < steps; i++ {
			p.step(base.Add(time.Duration(i) * time.Second))
		}
	}()
	go func() {
		defer writers.Done()
		ctx := context.Background()
		seq := []asterismv1.ContainerAction{
			asterismv1.ContainerAction_CONTAINER_ACTION_PAUSE,
			asterismv1.ContainerAction_CONTAINER_ACTION_UNPAUSE,
			asterismv1.ContainerAction_CONTAINER_ACTION_STOP,
			asterismv1.ContainerAction_CONTAINER_ACTION_START,
			asterismv1.ContainerAction_CONTAINER_ACTION_RESTART,
		}
		for i := 0; i < actionLoops; i++ {
			for _, id := range []string{apiID, workerID} {
				for _, a := range seq {
					c, err := p.ContainerAction(ctx, a, id, 0)
					if err != nil {
						t.Errorf("action %v on %s: %v", a, id, err)
						return
					}
					c.State = asterismv1.ContainerState_CONTAINER_STATE_DEAD // caller-owned
				}
			}
		}
	}()

	for r := 0; r < readers; r++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for {
				readEverything(t, p)
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

	// Reader scribbles must not have leaked into collector state.
	for _, w := range p.Workloads() {
		if w.State == "scribbled" || w.CpuUtil < 0 {
			t.Fatalf("reader mutation leaked into workload %s: state=%q cpu=%v", w.Id, w.State, w.CpuUtil)
		}
	}
	for _, c := range p.DockerSnapshot().Containers {
		if (c.ContainerId == apiID || c.ContainerId == workerID) && c.State == asterismv1.ContainerState_CONTAINER_STATE_DEAD {
			t.Fatalf("action result mutation leaked into container %s", c.Name)
		}
	}
}

// TestGettersReturnCallerOwnedCopies is the deterministic counterpart: it
// fails without -race when getters share internal pointers.
func TestGettersReturnCallerOwnedCopies(t *testing.T) {
	p := newTestProvider(t)

	w, ok := p.Workload("ctr:postgres-primary")
	if !ok {
		t.Fatal("workload ctr:postgres-primary not found")
	}
	w.Name = "mutated"
	w.Ports[0].HostPort = 1
	w.Labels["asterism.category"] = "mutated"

	proj := p.Projects()[0]
	proj.Name = "mutated"
	proj.Workloads[0].Name = "mutated"

	p.Workloads()[0].Image = "mutated"
	p.Ports()[0].HostPort = 1
	p.IngressRoutes()[0].Upstreams[0].Address = "mutated"
	p.Edges()[0].Label = "mutated"
	p.Events()[0].Message = "mutated"
	p.Logs("ctr:postgres-primary")[0].Text = "mutated"

	if got, _ := p.Workload("ctr:postgres-primary"); got.Name != "postgres-primary" || got.Ports[0].HostPort != 5432 || got.Labels["asterism.category"] != "production" {
		t.Errorf("Workload() shares state: %v", got)
	}
	if got := p.Projects()[0]; got.Name == "mutated" || got.Workloads[0].Name == "mutated" {
		t.Errorf("Projects() shares state: %s / %s", got.Name, got.Workloads[0].Name)
	}
	if got := p.Workloads()[0]; got.Image == "mutated" {
		t.Error("Workloads() shares state")
	}
	if got := p.Ports()[0]; got.HostPort == 1 {
		t.Error("Ports() shares state")
	}
	if got := p.IngressRoutes()[0]; got.Upstreams[0].Address == "mutated" {
		t.Error("IngressRoutes() shares state")
	}
	if got := p.Edges()[0]; got.Label == "mutated" {
		t.Error("Edges() shares state")
	}
	if got := p.Events()[0]; got.Message == "mutated" {
		t.Error("Events() shares state")
	}
	if got := p.Logs("ctr:postgres-primary")[0]; got.Text == "mutated" {
		t.Error("Logs() shares state")
	}
	if got := p.Logs("ctr:unknown"); got == nil || len(got) != 0 {
		t.Errorf("Logs() for unknown workload = %v, want empty non-nil slice", got)
	}
}

func TestShowcaseScale(t *testing.T) {
	tests := []struct {
		name          string
		fleet         int
		minContainers int
		minProjects   int
	}{
		{"curated default", 0, 30, 4},
		{"fleet of twelve", 12, 70, 16},
		{"fleet is capped", 500, 150, 4 + MaxFleet},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewWithOptions(bus.New(16), ring.New(64), Options{Fleet: tt.fleet})
			snap := p.DockerSnapshot()
			if n := len(snap.Containers); n < tt.minContainers {
				t.Fatalf("containers = %d, want >= %d", n, tt.minContainers)
			}
			if n := len(p.Projects()); n < tt.minProjects || (tt.fleet > MaxFleet && n != 4+MaxFleet) {
				t.Fatalf("projects = %d, want >= %d", n, tt.minProjects)
			}
			if len(snap.Images) < 40 || len(snap.Volumes) < 30 || len(snap.Networks) < 15 || len(p.Events()) < 150 {
				t.Fatalf("images=%d volumes=%d networks=%d events=%d", len(snap.Images), len(snap.Volumes), len(snap.Networks), len(p.Events()))
			}
			if n := len(p.Logs("ctr:postgres-primary")); n < 300 {
				t.Fatalf("log lines = %d", n)
			}
			// Curated workloads keep their identity.
			for _, id := range []string{"ctr:nova-api", "ctr:postgres-primary", "unit:nginx.service", "ctr:mailer", "ctr:backup-nightly"} {
				if _, ok := p.Workload(id); !ok {
					t.Fatalf("curated workload %s missing", id)
				}
			}
			tags := map[string]bool{}
			for _, img := range snap.Images {
				for _, tag := range img.RepoTags {
					if tags[tag] {
						t.Fatalf("duplicate image tag %s", tag)
					}
					tags[tag] = true
				}
			}
			ids := map[string]bool{}
			for _, c := range snap.Containers {
				if ids[c.Id] || ids[c.ContainerId] {
					t.Fatalf("duplicate container id %s/%s", c.Id, c.ContainerId)
				}
				ids[c.Id], ids[c.ContainerId] = true, true
			}
			portIDs := map[string]bool{}
			for _, pb := range p.Ports() {
				if portIDs[pb.Id] {
					t.Fatalf("duplicate port id %s", pb.Id)
				}
				portIDs[pb.Id] = true
			}
			eventIDs := map[string]bool{}
			for _, e := range p.Events() {
				if eventIDs[e.Id] {
					t.Fatalf("duplicate event id %s", e.Id)
				}
				eventIDs[e.Id] = true
			}
		})
	}
}

func TestDemoIconsAreValidAndDeterministic(t *testing.T) {
	p := &Provider{}
	for _, dir := range []string{"/srv/apps/nova-core", "/srv/apps/analytics-hub"} {
		ref, ok := p.DirIcon(dir)
		if !ok {
			t.Fatalf("no demo icon for %s", dir)
		}
		data, err := p.ReadDirIcon(context.Background(), ref)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := favicon.Validate(data, "", favicon.MaxFaviconBytes); err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		again, _ := p.DirIcon(dir)
		if again.SHA256 != ref.SHA256 || !ref.Generated {
			t.Fatalf("%s: not deterministic", dir)
		}
	}
	if _, ok := p.DirIcon("/srv/staging/lab"); ok {
		t.Fatal("staging-lab should keep its monogram")
	}
}
