package docker

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/prabhavalabs/asterism/internal/agent/collect/collecttest"
	"github.com/prabhavalabs/asterism/internal/agent/collect/redact"
	"github.com/prabhavalabs/asterism/internal/agent/collect/targets"
	"github.com/prabhavalabs/asterism/internal/agent/config"
	"github.com/prabhavalabs/asterism/internal/agent/dockerapi"
	"github.com/prabhavalabs/asterism/internal/agent/dockerapi/dockertest"
	"github.com/prabhavalabs/asterism/internal/agent/state"
	v1 "github.com/prabhavalabs/asterism/internal/gen/asterism/v1"
)

const (
	fixtures = "../../../collect/dockercli/testdata/engine"
	secret   = "s3cr3t-value"
	idA      = "317e4d06ef07a1453738080b7345f935f359dd2c0521ffffcf9bb62ab05fed25"
	idB      = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
)

func never(time.Duration) <-chan time.Time { return make(chan time.Time) }

type rig struct {
	handled chan struct{}
	src     *Source
	eng     *dockertest.Engine
	reg     *targets.Registry
	ctx     context.Context
	stop    func()
	done    chan struct{}
}

func (r *rig) wait(n int) {
	for range n {
		<-r.handled
	}
}

func newRig(t *testing.T, sink state.Sink, mutate func(*config.Config)) *rig {
	t.Helper()
	eng := dockertest.New(t, fixtures, dockerapi.Allowed)
	cl, err := dockerapi.New(eng.Endpoint, dockerapi.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Redaction.ExtraSecretKeys = []string{`^com\.example\.upstream$`}
	if mutate != nil {
		mutate(cfg)
	}
	red, err := redact.New(cfg.Redaction)
	if err != nil {
		t.Fatal(err)
	}
	reg := targets.New()
	s := New(Options{Docker: cfg.Sources.Docker, Interval: cfg.Intervals, Logs: cfg.Sources.Logs, Client: cl, Registry: reg, Redactor: red, After: never})
	now := func() time.Time { return time.Date(2026, 11, 24, 23, 0, 0, 0, time.UTC) }
	if _, err := s.Init(context.Background(), state.Env{Config: cfg, Now: now}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &rig{src: s, eng: eng, reg: reg, ctx: ctx, done: make(chan struct{}), handled: make(chan struct{}, 64)}
	s.handled = func() { r.handled <- struct{}{} }
	go func() {
		defer close(r.done)
		_ = s.Run(ctx, sink)
	}()
	r.stop = func() { cancel(); <-r.done }
	t.Cleanup(r.stop)
	<-s.Ready()
	<-eng.EventsConnected()
	return r
}

// TestNoSecretInAnyFrame is the redaction golden test: every fixture secret
// (env value, secret labels, command flag, short-flag credentials in a
// CMD-SHELL healthcheck, healthcheck output, URL
// credentials, log-driver options, engine proxy credentials, volume and
// network options, event attributes) must never appear in any encoded
// frame the agent sends.
func TestNoSecretInAnyFrame(t *testing.T) {
	store := state.NewStore(state.Options{})
	r := newRig(t, store.Sink("docker.run"), nil)
	// An event round trip, so event-driven inspects are covered too.
	ev, _ := os.ReadFile(fixtures + "/events.ndjson")
	lines := bytes.Split(bytes.TrimSpace(ev), []byte("\n"))
	for _, line := range lines {
		r.eng.PushEvent(line)
	}
	r.wait(len(lines))
	r.src.RefreshDiskUsage(r.ctx)
	r.stop()

	snap, _ := store.TakeInventory(0, true)
	if len(snap.GetContainers()) != 2 {
		t.Fatalf("containers in snapshot: %d", len(snap.GetContainers()))
	}
	frames := []*v1.AgentFrame{
		{Payload: &v1.AgentFrame_Snapshot{Snapshot: snap}},
		{Payload: &v1.AgentFrame_Events{Events: &v1.AgentEvents{Events: store.TakeEvents()}}},
		{Payload: &v1.AgentFrame_Metrics{Metrics: store.TakeMetrics()}},
	}
	for _, f := range frames {
		b, err := proto.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(secret)) {
			t.Fatalf("encoded %T frame contains the fixture secret", f.Payload)
		}
	}
	shop := snap.GetContainers()[1]
	if shop.Name != "shop-api-1" || shop.Labels["com.example.upstream"] != "" || shop.RedactedLabelCount != 3 {
		t.Fatalf("extra_secret_keys not applied: %v", shop.Labels)
	}
	// Short-flag credentials inside a shell string are redacted in place;
	// ports (ssh -p 2222) and the rest of the probe stay readable.
	wantTest := "redis-cli -a [REDACTED] ping && mysqladmin ping -uroot -p[REDACTED] && " +
		"curl -fsu admin:[REDACTED] http://localhost:8080/health && sshpass -p [REDACTED] ssh -p 2222 probe@db true"
	if got := shop.GetHealthcheck().GetTest(); len(got) != 2 || got[1] != wantTest {
		t.Fatalf("healthcheck test %q", got)
	}
	if strings.Join(shop.EnvKeys, ",") != "DB_PASSWORD,PATH,PORT" {
		t.Fatalf("env keys %v", shop.EnvKeys)
	}
}

func TestEventDrivenInventory(t *testing.T) {
	sink := collecttest.New()
	r := newRig(t, sink, nil)
	cs := sink.Snapshot()
	if len(cs) != 2 || cs["ctr:shop-api-1"] == nil || cs["ctr:cache-redis"] == nil {
		t.Fatalf("initial inventory %v", cs)
	}
	if r.eng.Count("GET", `/containers/`+idA[:0]+`[^/]*buildx`) != 0 || r.eng.Count("GET", "buildx") != 0 {
		t.Fatal("excluded container was inspected")
	}
	if c, ok := r.reg.ContainerByID(idA); !ok || c.Pid != 4242 || !c.Running || !c.OwnNetns || c.HostPorts[18080] != 8080 {
		t.Fatalf("registry target %+v", c)
	}
	if c, ok := r.reg.ContainerByID(idB); !ok || c.Running || c.OwnNetns {
		t.Fatalf("redis target %+v", c)
	}
	before := r.eng.Count("GET", `/containers/[^/]+/json$`)
	r.eng.PushEvent([]byte(`{"Type":"container","Action":"exec_start: sh","Actor":{"ID":"` + idA + `"}}`))
	r.eng.PushEvent([]byte(`{"Type":"container","Action":"health_status: healthy","Actor":{"ID":"` + idA + `","Attributes":{"name":"shop-api-1"}}}`))
	// Destroy of the redis container removes it without an inspect.
	r.eng.PushEvent([]byte(`{"Type":"container","Action":"destroy","Actor":{"ID":"` + idB + `"}}`))
	// A sentinel event we can wait for.
	r.eng.RemoveContainer("deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	r.eng.PushEvent([]byte(`{"Type":"container","Action":"start","Actor":{"ID":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}}`))
	r.wait(4)
	r.stop()
	after := r.eng.Count("GET", `/containers/[^/]+/json$`)
	if after-before != 2 { // health_status (one inspect) + sentinel (404)
		t.Fatalf("event-driven inspects: %d", after-before)
	}
	if _, ok := sink.Snapshot()["ctr:cache-redis"]; ok {
		t.Fatal("destroyed container still present")
	}
	if _, ok := r.reg.ContainerByID(idB); ok {
		t.Fatal("destroyed container still in registry")
	}
	if v := r.eng.Violations(); len(v) != 0 {
		t.Fatalf("non allow-listed endpoints: %+v", v)
	}
	if r.eng.Count("GET", "/stats") != 0 {
		t.Fatal("docker stats must never be called")
	}
}

func TestResyncSafetyNet(t *testing.T) {
	sink := collecttest.New()
	r := newRig(t, sink, nil)
	inspects := r.eng.Count("GET", `/containers/[^/]+/json$`)
	if err := r.src.Resync(r.ctx); err != nil {
		t.Fatal(err)
	}
	// The list matches what is known (state and health): no new inspects.
	// shop-api-1 is listed "Up 3 hours (healthy)" and inspected healthy;
	// cache-redis is exited in both.
	if n := r.eng.Count("GET", `/containers/[^/]+/json$`); n != inspects {
		t.Fatalf("unchanged resync inspected %d containers", n-inspects)
	}
	// The engine forgets redis: the list no longer has it -> removed.
	r.eng.SetBody("/containers/json", []byte(`[{"Id":"`+idA+`","Names":["/shop-api-1"],"State":"running","Status":"Up 3 hours (healthy)","Labels":{}}]`))
	if err := r.src.Resync(r.ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := sink.Snapshot()["ctr:cache-redis"]; ok {
		t.Fatal("container missing from the list was not removed")
	}
	// Engine down: one "unavailable" engine update.
	r.eng.SetStatus("GET /containers/json", 500)
	_ = r.src.Resync(r.ctx)
	_ = r.src.Resync(r.ctx)
	if e := sink.Docker.Engine; e == nil || e.Available {
		t.Fatalf("engine availability %v", e)
	}
}

func TestResourcesHashedAndDiskUsage(t *testing.T) {
	sink := collecttest.New()
	r := newRig(t, sink, nil)
	n := sink.CallCount("ReplaceDockerResources")
	if n != 1 || len(sink.Docker.Images) != 3 || len(sink.Docker.Volumes) != 2 || len(sink.Docker.Networks) != 4 {
		t.Fatalf("initial resources: calls=%d %+v", n, sink.Docker)
	}
	if img := sink.Docker.Images[0]; img.Containers != 1 || img.UsedBy[0] != "shop-api-1" {
		t.Fatalf("image usage %v", img)
	}
	if nw := sink.Docker.Networks[0]; nw.Name != "shop_default" || len(nw.Containers) != 1 {
		t.Fatalf("network attachments %v", nw)
	}
	r.src.RefreshResources(r.ctx)
	if sink.CallCount("ReplaceDockerResources") != 1 {
		t.Fatal("unchanged resources re-sent")
	}
	r.src.RefreshDiskUsage(r.ctx)
	if sink.Docker.DiskUsage == nil || len(sink.Docker.DiskUsage.Categories) != 4 {
		t.Fatalf("disk usage %v", sink.Docker.DiskUsage)
	}
	if sink.Docker.Volumes[0].SizeBytes != 402935167 {
		t.Fatalf("volume size not applied: %v", sink.Docker.Volumes[0])
	}
	if v := r.eng.Violations(); len(v) != 0 {
		t.Fatalf("violations %+v", v)
	}
}

func TestIncludeExcludeGroupBy(t *testing.T) {
	d := config.Default().Sources.Docker
	if !Excluded(d, "buildx_buildkit_default", nil) || !Excluded(d, "x", map[string]string{"asterism.ignore": "true"}) || Excluded(d, "x", map[string]string{"asterism.ignore": "false"}) {
		t.Fatal("default excludes")
	}
	d.Include.Names = []string{"shop-*"}
	if Excluded(d, "shop-api-1", nil) || !Excluded(d, "grafana", nil) {
		t.Fatal("include names")
	}
	d.Include = config.Selector{Labels: map[string]string{"team": ""}}
	if Excluded(d, "a", map[string]string{"team": "x"}) || !Excluded(d, "a", nil) {
		t.Fatal("include label presence")
	}

	sink := collecttest.New()
	newRig(t, sink, func(c *config.Config) { c.Sources.Docker.GroupBy = "label:asterism.role" })
	cs := sink.Snapshot()
	if cs["ctr:cache-redis"].ProjectId != "proj:cache" || cs["ctr:shop-api-1"].ProjectId != "proj:standalone" {
		t.Fatalf("group_by label: %s %s", cs["ctr:cache-redis"].ProjectId, cs["ctr:shop-api-1"].ProjectId)
	}
	sink2 := collecttest.New()
	newRig(t, sink2, func(c *config.Config) { c.Sources.Docker.GroupBy = "none" })
	if p := sink2.Snapshot()["ctr:shop-api-1"].ProjectId; p != "proj:standalone" {
		t.Fatalf("group_by none: %s", p)
	}
}

func TestLogs(t *testing.T) {
	sink := collecttest.New()
	r := newRig(t, sink, nil)
	var lines []*v1.LogLine
	var eof bool
	r.src.Logs(r.ctx, &v1.LogRequest{RequestId: "r1", WorkloadId: "ctr:shop-api-1", Tail: 5000}, func(ch *v1.LogChunk) bool {
		if ch.Error != "" {
			t.Fatalf("error %s", ch.Error)
		}
		lines = append(lines, ch.Lines...)
		eof = eof || ch.Eof
		return true
	})
	if !eof || len(lines) != 3 || lines[1].Stream != "stderr" || strings.Contains(lines[1].Text, secret) || lines[0].TsMs == 0 {
		t.Fatalf("lines %v eof %v", lines, eof)
	}
	h := r.eng.Hits()
	var q string
	for _, x := range h {
		if strings.HasSuffix(x.Path, "/logs") {
			q = x.Query
		}
	}
	if !strings.Contains(q, "tail=500") {
		t.Fatalf("tail not capped: %s", q)
	}
	var errMsg string
	r.src.Logs(r.ctx, &v1.LogRequest{RequestId: "r2", WorkloadId: "unit:nginx.service"}, func(ch *v1.LogChunk) bool { errMsg = ch.Error; return true })
	if errMsg == "" {
		t.Fatal("non-container workload accepted")
	}
}

func TestInitFailsWhenEngineDown(t *testing.T) {
	cl, _ := dockerapi.New("unix:///nonexistent/docker.sock", dockerapi.Options{Timeout: time.Second})
	s := New(Options{Client: cl})
	if _, err := s.Init(context.Background(), state.Env{}); err == nil {
		t.Fatal("init must fail without an engine")
	}
}
